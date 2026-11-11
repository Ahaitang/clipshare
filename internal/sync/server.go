// Package sync 实现节点间的 HTTP 收发、房间鉴权与广播。
//
// 协议见 docs/superpowers/specs/2026-11-11-lan-clipboard-design.md §6：
// 明文 HTTP/1.1 + JSON，内网信任模型；roomKey 只以 Bearer 形式出现在请求头，
// 出网的房间标识是它的 sha256 前 12 位（X-Room）。
package sync

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"clipshare/internal/history"
	"clipshare/internal/model"
)

const (
	// DefaultMaxBytes 是调用方未指定时的内容上限，与 config.DefaultItemMaxBytes 一致。
	DefaultMaxBytes = 262144
	// envelopeSlack 是 JSON 信封（id/origin/originName/recvAt 等字段）允许的额外字节。
	// 内容本身按 MaxBytes 校验，请求体上限要留出这部分余量。
	envelopeSlack = 4096
)

// ServerConfig 是服务端的固定配置。
type ServerConfig struct {
	// RoomKey 是房间密钥，用于校验 Authorization: Bearer。
	RoomKey string
	// RoomHash 是房间标识（model.RoomHash(RoomKey)），用于校验 X-Room。
	// 为空时由 RoomKey 推导，避免两处配置不一致。
	RoomHash string
	NodeID   string
	NodeName string
	Version  string
	// MaxBytes 是单条内容上限；<=0 时用 DefaultMaxBytes。
	MaxBytes int
}

// Server 是对等节点的 HTTP 服务端：接收 /v1/items，暴露 /v1/health。
//
// 它不自己决定「收到之后干什么」——apply 是 engine 注入的回调，
// 因此 handler 可以直接用 httptest 单测，不需要起真实端口。
//
// 安全边界：空 RoomKey 是配置错误，服务端对 /v1/items 一律 fail-closed
// （回 500 拒绝接收），绝不因为「空串等于空串」而放行未鉴权请求；
// /v1/health 本来免鉴权，不受影响。构造方应在启动时保证 RoomKey 非空
// （config.Validate 已强制），这里只是把「万一配错」的后果限制为拒绝服务。
type Server struct {
	cfg   ServerConfig
	store *history.Store
	apply func(model.Entry)
}

// NewServer 组装服务端。store 用于内容去重（可为 nil），apply 可为 nil。
//
// RoomKey 为空时不会 panic，但该 Server 的 /v1/items 会对所有请求回 500：
// 没有密钥就没有可校验的凭据，只能拒绝服务（详见 handleItems 与 Server 文档）。
func NewServer(cfg ServerConfig, store *history.Store, apply func(model.Entry)) *Server {
	if cfg.MaxBytes <= 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.RoomHash == "" {
		cfg.RoomHash = model.RoomHash(cfg.RoomKey)
	}
	return &Server{cfg: cfg, store: store, apply: apply}
}

// Handler 返回可直接挂到 http.Server 或 httptest 的 handler。
//
// 用 Go 1.22+ 的方法路由模式，非 POST / GET 的请求由 mux 自动回 405。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/items", s.handleItems)
	mux.HandleFunc("GET /v1/health", s.handleHealth)
	return mux
}

// handleItems 处理一条入站条目（spec §6）。
//
// 状态码约定：401 roomKey 不符、403 房间不符、400 内容为空/超限/JSON 非法、
// 202 其余情况（含内容去重命中——去重也是「已接受但不处理」）。
func (s *Server) handleItems(w http.ResponseWriter, r *http.Request) {
	// 配置错误优先于鉴权：空 RoomKey 时「空串 == 空串」会让不带 Authorization 的
	// 请求通过 constantTimeEqual，这是不可接受的 fail-open。没有密钥就没有可校验的
	// 凭据，只能 fail-closed 拒绝接收；回 500 而不是 401，避免被误判成客户端密钥错。
	if s.cfg.RoomKey == "" {
		writeError(w, http.StatusInternalServerError, "服务端未配置 roomKey，拒绝接收（配置错误：请检查 config.json 的 roomKey 并重启）")
		return
	}
	if !constantTimeEqual(bearerToken(r), s.cfg.RoomKey) {
		writeError(w, http.StatusUnauthorized, "roomKey 不匹配")
		return
	}
	if !constantTimeEqual(strings.TrimSpace(r.Header.Get("X-Room")), s.cfg.RoomHash) {
		writeError(w, http.StatusForbidden, "X-Room 与本地 roomHash 不一致（多半是两台机器配了不同的 roomKey）")
		return
	}

	// 请求体上限 = 内容上限 + 信封余量；多读 1 字节用于识别「超了」。
	limit := int64(s.cfg.MaxBytes) + envelopeSlack
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	if int64(len(body)) > limit {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("请求体超过 %d 字节（内容上限 %d）", limit, s.cfg.MaxBytes))
		return
	}

	var entry model.Entry
	if err := json.Unmarshal(body, &entry); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的条目 JSON")
		return
	}

	entry.Content = model.Normalize(entry.Content)
	if entry.Content == "" {
		writeError(w, http.StatusBadRequest, "内容为空")
		return
	}
	if len(entry.Content) > s.cfg.MaxBytes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("内容 %d 字节，超过上限 %d", len(entry.Content), s.cfg.MaxBytes))
		return
	}

	// 排序只用本地时间：完全不信任发送方的 recvAt。
	entry.RecvAt = time.Now()
	// ID 按规范由 origin+content 推导，跨机转发保持稳定，同时不受发送方伪造影响。
	entry.ID = model.NewEntry(entry.Origin, entry.OriginName, entry.Content, entry.RecvAt).ID

	added := true
	if s.store != nil {
		added = s.store.Add(entry)
	}
	// store.Add 返回 false 说明同内容已在历史里（S6）：202 且不触发 apply。
	if added && s.apply != nil {
		s.apply(entry)
	}
	w.WriteHeader(http.StatusAccepted)
}

// healthResponse 是 /v1/health 的响应体（spec §6：免鉴权，用于 seeds 探活与房间校验）。
type healthResponse struct {
	NodeID   string `json:"nodeId"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	RoomHash string `json:"roomHash"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		NodeID:   s.cfg.NodeID,
		Name:     s.cfg.NodeName,
		Version:  s.cfg.Version,
		RoomHash: s.cfg.RoomHash,
	})
}

// bearerToken 从 Authorization 头里取出 Bearer token；格式不对时返回空串。
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

// constantTimeEqual 是定长比较，避免按字节提前返回泄露 key 前缀。
//
// 注意：空串与空串相等。因此调用方必须先排除「期望值为空」的情况
// （handleItems 已在比较前拦下空 RoomKey），不能靠这个函数兜底。
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
