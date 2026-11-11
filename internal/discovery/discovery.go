// Package discovery 找出同房间的 peers：mDNS 公告/订阅 + 静态 seeds 探活。
//
// 两条腿互相独立：mDNS 适用于普通局域网；禁多播的网络只能配 seeds（`mdns:false`），
// 因此 seeds 这条路径必须在 MDNS=false 时照常工作。
//
// 结果通过 onUpdate 回调推送（按 deviceId 去重、排除自身、稳定排序），
// 只在 peer 集合真正变化时触发，避免界面无谓重绘。
package discovery

import (
	"context"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"

	"clipshare/internal/config"
)

const (
	// serviceType 是 mDNS 服务类型（design §7）。
	serviceType = "_clipshare._tcp"
	domain      = "local."

	// seedInterval 是静态 seeds 的探活周期（design §7）。
	seedInterval = 30 * time.Second
	// seedProbeTimeout 是单次 /v1/health 的超时。
	seedProbeTimeout = 2 * time.Second

	// mdnsTTL 是 peer 在没有任何新 mDNS 记录后的存活时间。
	// zeroconf v1 只通知「出现」不通知「消失」，因此靠本地过期回收。
	mdnsTTL = 120 * time.Second
	// mdnsResweep 是过期扫描周期。
	mdnsResweep = 10 * time.Second
	// mdnsRetryDelay 是 Browse 异常退出后的重试间隔。
	mdnsRetryDelay = 5 * time.Second
)

// Peer 是一个可同步的对端节点。
type Peer struct {
	NodeID  string
	Name    string
	BaseURL string // 形如 http://192.168.1.7:47100，无尾斜杠
}

// Config 是 Finder 的启动参数。
type Config struct {
	NodeID   string
	NodeName string
	RoomHash string
	Version  string
	Port     int
	Seeds    []string
	MDNS     bool
}

// Finder 持续维护同房间 peer 集合。
//
// Start 之后由后台 goroutine（mDNS 订阅、seeds 探活、过期扫描）驱动，
// Stop 会等所有 goroutine 退出，因此可以安全地重复创建/销毁（MDNS 开关切换）。
type Finder struct {
	cfg      Config
	onUpdate func([]Peer)

	client       *http.Client
	logf         func(string, ...any)
	seedInterval time.Duration
	now          func() time.Time

	mu        sync.Mutex
	mdnsPeers map[string]Peer
	mdnsExp   map[string]time.Time
	seedPeers map[string]Peer
	published []Peer
	lastErr   error

	notifyMu sync.Mutex // 串行化 onUpdate，避免并发回调
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	reg      *zeroconf.Server
}

// New 创建 Finder。onUpdate 可为 nil；被调用时会拿到一份新切片，可安全持有。
func New(cfg Config, onUpdate func([]Peer)) *Finder {
	return &Finder{
		cfg:          cfg,
		onUpdate:     onUpdate,
		client:       &http.Client{Timeout: seedProbeTimeout},
		logf:         log.Printf,
		seedInterval: seedInterval,
		now:          time.Now,
		mdnsPeers:    map[string]Peer{},
		mdnsExp:      map[string]time.Time{},
		seedPeers:    map[string]Peer{},
	}
}

// SetLogger 替换日志输出；nil 表示静默。engine 用它把发现日志并入统一日志。
func (f *Finder) SetLogger(logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	f.logf = logf
}

// Start 启动 mDNS 与 seeds 两条发现腿。mDNS 公告失败不算致命错误：
// 记一条告警后继续走 seeds（缺多播/缺 avahi 的机器上 seeds 是唯一路径）。
func (f *Finder) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel

	if f.cfg.MDNS {
		if err := f.startMDNS(ctx); err != nil {
			// 不返回错误：mDNS 不可用不应阻止 seeds 生效。
			f.setErr(err)
			f.logf("[discovery] mDNS 不可用，仅使用 seeds: %v", err)
		}
	}
	if len(f.cfg.Seeds) > 0 {
		f.wg.Add(1)
		go f.seedLoop(ctx)
	}
	return nil
}

// Stop 停止所有后台 goroutine 并注销 mDNS 公告。可重复调用。
func (f *Finder) Stop() {
	if f.cancel == nil {
		return
	}
	f.cancel()
	f.cancel = nil
	if f.reg != nil {
		f.reg.Shutdown()
		f.reg = nil
	}
	f.wg.Wait()
}

// Peers 返回当前 peer 集合（按名称、NodeID 稳定排序）。
func (f *Finder) Peers() []Peer {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Peer, len(f.published))
	copy(out, f.published)
	return out
}

// LastError 返回最近一次发现链路的错误（只保留最后一条）。
func (f *Finder) LastError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastErr
}

// ProbeNow 立即对所有 seeds 探活一次，不等下一个 30s 周期。
//
// 供「刷新节点」这类显式动作以及测试使用；MDNS=false 时尤其有用。
func (f *Finder) ProbeNow() {
	if len(f.cfg.Seeds) == 0 {
		return
	}
	f.probeSeeds(context.Background())
}

func (f *Finder) setErr(err error) {
	f.mu.Lock()
	f.lastErr = err
	f.mu.Unlock()
}

// seedLoop 立刻探活一次，之后每 seedInterval 一轮。
func (f *Finder) seedLoop(ctx context.Context) {
	defer f.wg.Done()
	f.probeSeeds(ctx)
	t := time.NewTicker(f.seedInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.probeSeeds(ctx)
		}
	}
}

// probeSeeds 并发探测所有 seeds，见 seeds.go。

// mergeLocked 合并两条腿的结果并去重：以 deviceId 为键，排除自身；mDNS 优先
// （带真实端口，seeds 也可能填的是同一节点）。
func (f *Finder) mergeLocked() []Peer {
	merged := make(map[string]Peer, len(f.mdnsPeers)+len(f.seedPeers))
	for id, p := range f.seedPeers {
		if id == f.cfg.NodeID || id == "" {
			continue
		}
		merged[id] = p
	}
	for id, p := range f.mdnsPeers {
		if id == f.cfg.NodeID || id == "" {
			continue
		}
		merged[id] = p
	}
	out := make([]Peer, 0, len(merged))
	for _, p := range merged {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

// publish 重算 peer 集合；仅当内容变化时回调 onUpdate。回调在锁外执行。
func (f *Finder) publish() {
	f.mu.Lock()
	next := f.mergeLocked()
	if samePeers(next, f.published) {
		f.mu.Unlock()
		return
	}
	f.published = next
	f.mu.Unlock()

	f.notifyMu.Lock()
	defer f.notifyMu.Unlock()
	if f.onUpdate != nil {
		f.onUpdate(append([]Peer(nil), next...))
	}
}

func samePeers(a, b []Peer) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sweepMDNS 回收长期没有新记录的 mDNS peer（zeroconf v1 不提供 goodbye 回调）。
func (f *Finder) sweepMDNS() {
	now := f.now()
	f.mu.Lock()
	changed := false
	for id, exp := range f.mdnsExp {
		if now.After(exp) {
			delete(f.mdnsExp, id)
			delete(f.mdnsPeers, id)
			changed = true
		}
	}
	f.mu.Unlock()
	if changed {
		f.publish()
	}
}

// addrURL 由 host + port 拼出 BaseURL；IPv6 走 JoinHostPort 保证方括号。
func addrURL(host string, port int) string {
	if host == "" || port <= 0 || port > 65535 {
		return ""
	}
	return config.BaseURL(net.JoinHostPort(host, strconv.Itoa(port)))
}

// sleepCtx 睡 d 时长；ctx 先取消则返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
