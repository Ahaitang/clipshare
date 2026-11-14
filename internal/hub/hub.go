// Package hub 实现「中心点 A」：收下同一房间内各节点的上行条目，再分发给订阅者。
//
// 与局域网模式的根本区别：**hub 的本质就是转发**。局域网版靠「入站不转发」来避免
// A→B→A 回环（见 sync.Server 与 TestIntegrationNoLoopback）；hub 只把「客户端主动
// 上行提交」的条目入队下发，回环由客户端侧保证（收到即 apply + MarkSeen，
// 且忽略 origin == 自己 deviceId 的条目）。
package hub

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clipshare/internal/model"
)

// Config 是 hub 的运行参数。
type Config struct {
	RoomKey  string // 校验 Authorization: Bearer
	RoomHash string // 校验 X-Room（为空时由 RoomKey 推导）
	NodeID   string
	NodeName string
	Version  string
	// Keep 是每房间保留的最近条目数（0 = 纯转发，不做补偿）。
	Keep int
	// MaxBytes 单条内容上限。
	MaxBytes int
	// RatePerMin 是每客户端 IP 每分钟允许的上行条数（0 = 不限）。
	RatePerMin int
	// OnEntry 可选：中心点自己也有剪贴板时，用它把条目应用出去。
	OnEntry func(model.Entry)
	// Logf 可选日志。
	Logf func(format string, args ...any)
}

// feedItem 是下发给客户端的一条记录：带房间内递增序号。
type feedItem struct {
	Seq   uint64      `json:"seq"`
	Entry model.Entry `json:"entry"`
}

// Hub 是房间状态的持有者。并发安全。
type Hub struct {
	cfg Config

	mu      sync.Mutex
	seq     uint64
	ring    []feedItem
	notify  chan struct{} // 关闭即唤醒所有等待者（见 broadcast）
	waiters int32         // 正在长轮询的客户端数（= 「在线节点」）

	rateMu sync.Mutex
	rate   map[string]*rateWindow
}

type rateWindow struct {
	start time.Time
	count int
}

// New 构造 hub。
func New(cfg Config) *Hub {
	if cfg.RoomHash == "" {
		cfg.RoomHash = model.RoomHash(cfg.RoomKey)
	}
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = 256 << 10
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Hub{cfg: cfg, notify: make(chan struct{}), rate: map[string]*rateWindow{}}
}

// Handler 返回 HTTP 路由。
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/items", h.postItem)
	mux.HandleFunc("GET /v1/feed", h.feed)
	mux.HandleFunc("GET /v1/health", h.health)
	return mux
}

// AppendForTest 直接入队一条条目，仅供测试构造「已有积压」的场景。
// 正常路径永远走 POST /v1/items（鉴权 + 限流 + 校验）。
func (h *Hub) AppendForTest(entry model.Entry) uint64 { return h.append(entry) }

// Stats 返回当前在线订阅者数与房间最新序号，供启动日志与 hub-info 使用。
func (h *Hub) Stats() (peers int, lastSeq uint64) {
	h.mu.Lock()
	seq := h.seq
	h.mu.Unlock()
	return int(atomic.LoadInt32(&h.waiters)), seq
}

// postItem 接收上行条目：鉴权 → 限流 → 校验 → 入队 → 唤醒订阅者。
func (h *Hub) postItem(w http.ResponseWriter, r *http.Request) {
	if !h.authOK(r) {
		writeErr(w, http.StatusUnauthorized, "房间密钥或房间标识不匹配")
		return
	}
	if !h.allow(r.RemoteAddr) {
		writeErr(w, http.StatusTooManyRequests, "上行太频繁，请稍后重试")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(h.cfg.MaxBytes)+4096))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	var entry model.Entry
	if err := json.Unmarshal(body, &entry); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是合法的 Entry JSON")
		return
	}
	if strings.TrimSpace(entry.Content) == "" {
		writeErr(w, http.StatusBadRequest, "content 不能为空")
		return
	}
	if len(entry.Content) > h.cfg.MaxBytes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("内容超过上限 %d 字节", h.cfg.MaxBytes))
		return
	}
	if entry.RecvAt.IsZero() {
		entry.RecvAt = time.Now()
	}
	if entry.ID == "" {
		entry.ID = model.NewEntry(entry.Origin, entry.OriginName, entry.Content, entry.RecvAt).ID
	}

	seq := h.append(entry)
	if h.cfg.OnEntry != nil {
		// 中心点自己也有剪贴板时才需要应用；放到 goroutine 里避免拖慢上行响应。
		go h.cfg.OnEntry(entry)
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"seq": seq})
}

// feed 是下行读取：since 之后的新条目；wait 为长轮询挂起时长。
//
// 语义（与设计文档 §4 一致）：
//   - since 省略：只回游标，不回放历史（新客户端不该被灌一整队历史）；
//   - since 落后太多、已滑出保留窗口：同样只回游标（宁缺不做重放风暴）；
//   - wait 上限 5 秒，无新条目就挂到超时再回空列表。
func (h *Hub) feed(w http.ResponseWriter, r *http.Request) {
	if !h.authOK(r) {
		writeErr(w, http.StatusUnauthorized, "房间密钥或房间标识不匹配")
		return
	}
	q := r.URL.Query()
	wait := 0
	if v := q.Get("wait"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "wait 必须是非负整数毫秒")
			return
		}
		wait = n
	}
	if wait > 5000 {
		wait = 5000
	}

	sinceStr := strings.TrimSpace(q.Get("since"))
	if sinceStr == "" {
		h.writeFeed(w, nil, true)
		return
	}
	since, err := strconv.ParseUint(sinceStr, 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "since 必须是递增整数")
		return
	}

	// 游标异常（比服务端还大 = 服务端重启过；落后于窗口 = 中间漏太多）一律跳最新。
	if since > h.lastSeq() || h.tooOld(since) {
		h.writeFeed(w, nil, true)
		return
	}

	if wait > 0 {
		deadline := time.NewTimer(time.Duration(wait) * time.Millisecond)
		defer deadline.Stop()
		atomic.AddInt32(&h.waiters, 1)
		defer atomic.AddInt32(&h.waiters, -1)
		for {
			if items := h.since(since); len(items) > 0 {
				h.writeFeed(w, items, false)
				return
			}
			ch := h.notifyChan()
			select {
			case <-ch:
			case <-deadline.C:
				h.writeFeed(w, h.since(since), false)
				return
			case <-r.Context().Done():
				return
			}
		}
	}
	h.writeFeed(w, h.since(since), false)
}

// health 是免鉴权探活，供客户端与人工排障确认中心点是否在线、房间是否一致。
func (h *Hub) health(w http.ResponseWriter, r *http.Request) {
	peers, lastSeq := h.Stats()
	writeJSON(w, http.StatusOK, map[string]any{
		"nodeId":   h.cfg.NodeID,
		"name":     h.cfg.NodeName,
		"version":  h.cfg.Version,
		"roomHash": h.cfg.RoomHash,
		"role":     "hub",
		"peers":    peers,
		"lastSeq":  lastSeq,
	})
}

// ---- 内部 ----

func (h *Hub) authOK(r *http.Request) bool {
	// fail-closed：没有配置密钥就拒绝一切请求（空 == 空 会被定长比较判为相等）。
	if h.cfg.RoomKey == "" || h.cfg.RoomHash == "" {
		return false
	}
	if !constantTimeEqual(bearerToken(r), h.cfg.RoomKey) {
		return false
	}
	return constantTimeEqual(strings.TrimSpace(r.Header.Get("X-Room")), h.cfg.RoomHash)
}

func (h *Hub) allow(remoteAddr string) bool {
	if h.cfg.RatePerMin <= 0 {
		return true
	}
	ip := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		ip = host
	}
	now := time.Now()
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	win, ok := h.rate[ip]
	if !ok || now.Sub(win.start) > time.Minute {
		if len(h.rate) > 4096 { // 防 map 无界增长
			h.rate = map[string]*rateWindow{}
		}
		h.rate[ip] = &rateWindow{start: now, count: 1}
		return true
	}
	if win.count >= h.cfg.RatePerMin {
		return false
	}
	win.count++
	return true
}

func (h *Hub) append(entry model.Entry) uint64 {
	h.mu.Lock()
	h.seq++
	seq := h.seq
	if h.cfg.Keep > 0 {
		h.ring = append(h.ring, feedItem{Seq: seq, Entry: entry})
		if len(h.ring) > h.cfg.Keep {
			h.ring = append([]feedItem(nil), h.ring[len(h.ring)-h.cfg.Keep:]...)
		}
	}
	ch := h.notify
	h.notify = make(chan struct{})
	h.mu.Unlock()

	close(ch) // 唤醒全部长轮询等待者
	return seq
}

func (h *Hub) lastSeq() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

func (h *Hub) notifyChan() chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.notify
}

// tooOld 判断 since 是否已滑出保留窗口（此时只能跳最新）。
func (h *Hub) tooOld(since uint64) bool {
	if h.cfg.Keep <= 0 {
		return true // 不保留窗口：任何 since 都无法补偿
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ring) == 0 {
		return since < h.seq
	}
	return since+1 < h.ring[0].Seq
}

func (h *Hub) since(since uint64) []feedItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []feedItem
	for _, it := range h.ring {
		if it.Seq > since {
			out = append(out, it)
		}
	}
	return out
}

func (h *Hub) writeFeed(w http.ResponseWriter, items []feedItem, cursorOnly bool) {
	if items == nil {
		items = []feedItem{}
	}
	peers, lastSeq := h.Stats()
	resp := map[string]any{
		"items":   items,
		"lastSeq": lastSeq,
		"peers":   peers,
	}
	if cursorOnly {
		resp["reset"] = true // 客户端据此把自己的游标对齐到 lastSeq
	}
	writeJSON(w, http.StatusOK, resp)
}

func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// constantTimeEqual 是定长比较，避免按字节提前返回泄露 key 前缀（与 sync 包同口径）。
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// ErrNotConfigured 供调用方在 roomKey 缺失时给出明确提示。
var ErrNotConfigured = errors.New("hub 未配置房间密钥")
