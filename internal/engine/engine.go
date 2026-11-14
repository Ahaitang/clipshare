// Package engine 组装剪贴板、历史、同步与发现，并独占「同步语义」的实现。
//
// 同步行为规范见 docs/superpowers/specs/2026-11-11-lan-clipboard-design.md §5：
//
//	S1 本机复制      → 入历史（Origin=本机）+ 广播（暂停时不广播）
//	S2 收到远端条目  → 入历史由 sync.Server 负责；AutoApply 且未暂停时写剪贴板 + MarkSeen
//	S3 取回历史条目  → 写剪贴板 + MarkSeen，不广播
//	S4 发送当前剪贴板 → 强制广播一次
//	S5 暂停同步      → 不广播、不应用远端；本地复制仍入历史
//	S6 重复内容      → sync.Server 侧去重后不再回调，因此不会重复入历史/写剪贴板
//	S7 广播失败      → 记日志丢弃，不重试
//	S8 内容为空/超限 → 跳过，不广播、不入历史
//
// 核心不变量：任何由程序写入剪贴板的内容都不得被轮询误判成「用户复制」而回播。
// 实现方式是无状态的 poller.MarkSeen(content)（改写轮询器基线），而不是 suppress 标志位：
// 标志位依赖「被下一次变化消费」，若这次写入恰好与基线相同（轮询根本不会触发），
// 标志位就会滞留，之后用户真正复制同一段内容时会被误吞。MarkSeen 没有这个问题。
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"time"

	"clipshare/internal/clip"
	"clipshare/internal/config"
	"clipshare/internal/discovery"
	"clipshare/internal/history"
	"clipshare/internal/model"
	"clipshare/internal/sync"
)

const (
	// SnapshotRecent 是状态快照里携带的最近条目数（托盘菜单直接渲染这些）。
	SnapshotRecent = 10

	// shutdownTimeout 是 Stop 时等待 HTTP 连接收尾的上限。
	shutdownTimeout = 3 * time.Second

	// broadcastWarnThreshold 是「连续多少轮广播全部失败」后才给出可操作提示。
	// 用连续轮次而不是时间窗：一轮广播 = 一次用户复制，连续三轮全部失败基本可以排除偶发抖动。
	broadcastWarnThreshold = 3

	// broadcastWarnInterval 是同一节点的提示节流窗口（每个节点每分钟最多一条）。
	broadcastWarnInterval = 60 * time.Second
)

// Version 是协议握手与 /v1/health 上报的版本号。
//
// 必须是变量而不是常量：main 用 ldflags 注入真实版本（-X main.version=...）后，
// 会在调用 Start 之前用同一份值覆盖这里，保证 `clipshare --version` 打印的版本
// 与两台机器互相上报的版本永远一致。
//
// 默认值 0.1.0 只是独立运行 engine 包（单测、未注入版本的裸 go run）时的兜底。
// 唯一读取点是 Start 里的 sync.ServerConfig.Version（以及 discovery 的 Version 字段），
// 不要在别处再硬编码版本号。
var Version = "0.1.0"

// 对外可判定的错误（界面层可以直接展示 Error() 文本）。
var (
	// ErrEmptyClipboard 表示剪贴板没有文本内容（比如复制的是一张图）。
	ErrEmptyClipboard = errors.New("剪贴板没有文本内容")
	// ErrTooLarge 表示内容超过 itemMaxBytes（S8）。
	ErrTooLarge = errors.New("剪贴板内容超过单条上限")
	// ErrPaused 表示同步已暂停却要求强制广播（S4 需要先恢复同步）。
	ErrPaused = errors.New("同步已暂停")
	// ErrNotStarted 表示引擎尚未启动或已经停止。
	ErrNotStarted = errors.New("引擎未启动")
	// ErrAlreadyStarted 表示重复启动。
	ErrAlreadyStarted = errors.New("引擎已在运行")
)

// Engine 是 internal/engine 的唯一实现：它把组件装配成一台可运行的节点，
// 并实现 Controller 契约（tray 与 webui 只通过 Controller 使用它）。
type Engine struct {
	cfg    *config.Config
	store  *history.Store
	board  clip.Clipboard
	logf   func(string, ...any)
	cfgDir string

	mu        stdsync.RWMutex
	paused    bool
	autoApply bool
	mdns      bool
	lastErr   string
	started   bool

	server  *sync.Server
	httpSrv *http.Server
	ln      net.Listener
	bcast   *sync.Broadcaster
	poller  *clip.Poller

	// cfgMu 保护 e.cfg 的可变字段（AutoApply / MDNS）以及它的序列化。
	// tray 的每次点击都是独立 goroutine，两个开关同时点会一边改 cfg 一边 json.Marshal，
	// 所以「改字段」和「落盘」必须在同一把锁里完成。
	cfgMu stdsync.Mutex

	finderMu stdsync.Mutex
	finder   *discovery.Finder

	// lastProbeNano 是上次触发即时重探的时刻（UnixNano），用原子量做节流；
	// 不能用 stdlib sync：本文件已经把 internal/sync 导入为 sync。
	lastProbeNano atomic.Int64

	// publishers 是中继等额外上行通道（AddPublisher 注册）。
	pubMu      stdsync.Mutex
	publishers []func(model.Entry) error

	// 中继状态由 cmd 层注入（中继客户端不归 engine 管，但托盘/状态页要能看见）。
	relayMu        stdsync.Mutex
	relayURL       string
	relayEnabled   bool
	relayConnected func() bool

	// finderRebuildMu 串行化「停止旧 Finder + 启动新 Finder」，
	// 避免并发 SetMDNS 交错执行时旧 Finder 的 goroutine/mDNS 连接泄漏。
	finderRebuildMu stdsync.Mutex

	// bcMu 保护「广播持续失败」的聚合状态（按 peer 地址记连续失败轮次与上次告警时间）。
	// Stop 会把它清空，避免残留状态在下次 Start 时误报。这里只用时间戳比较，
	// 不起 timer/goroutine，因此不存在需要回收的后台资源。
	bcMu    stdsync.Mutex
	bcState map[string]*peerFailure

	// now 可注入（测试用），默认 time.Now。
	now func() time.Time

	wg stdsync.WaitGroup
}

// 编译期确认 Engine 实现了 t1 定下的界面契约。
var _ Controller = (*Engine)(nil)

// New 组装引擎（不启动任何 goroutine；Start 之后才开始工作）。
//
// clip 由调用方注入（真实适配器用 clip.New()，测试用假实现）；
// logf 为 nil 时退化为标准库 log。
func New(cfg *config.Config, store *history.Store, board clip.Clipboard, logf func(string, ...any)) *Engine {
	if logf == nil {
		logf = log.Printf
	}
	e := &Engine{
		cfg:       cfg,
		store:     store,
		board:     board,
		logf:      logf,
		cfgDir:    configDirFor(cfg),
		autoApply: cfg.AutoApply,
		mdns:      cfg.MDNS,
		bcState:   map[string]*peerFailure{},
		now:       time.Now,
	}
	if e.cfgDir == "" {
		e.setErr(errors.New("配置目录不可用，设置改动无法保存"))
	}
	return e
}

// configDirFor 决定「托盘开关要写回哪个目录」。
//
// 优先用 cfg.Dir —— 那是 config.Load(dir) 实际读到的目录（即 --config-dir 的值）。
// 直接推算 config.Dir() 会忽略调用方指定的目录，导致开关写回默认目录：
// 用户带 --config-dir 运行时会在默认目录凭空多出一份 config.json，
// 双进程实测（两个不同 --config-dir）还会互相覆盖。
// cfg.Dir 为空只在直接构造 &config.Config{...} 的旧用法或单测里出现，此时回退到默认目录。
func configDirFor(cfg *config.Config) string {
	if cfg != nil && cfg.Dir != "" {
		return cfg.Dir
	}
	dir, err := config.Dir()
	if err != nil {
		return ""
	}
	return dir
}

// Start 启动 HTTP 服务端、发现、剪贴板轮询。
//
// 端口被占用时返回带处置建议的错误（design §10）。
func (e *Engine) Start() error {
	// Validate 会读 AutoApply/MDNS，而托盘开关会并发写它们；这里与 saveConfigLocked 用同一把锁。
	e.cfgMu.Lock()
	err := e.cfg.Validate()
	e.cfgMu.Unlock()
	if err != nil {
		return err
	}
	if e.board == nil {
		return errors.New("engine: 剪贴板适配器为空")
	}
	if e.store == nil {
		return errors.New("engine: 历史仓库为空")
	}

	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return ErrAlreadyStarted
	}
	e.mu.Unlock()

	// 组件按「被依赖者先就绪」的顺序装配：HTTP handler 可能立刻被入站请求打到，
	// 那时 applyRemote 必须已经能看到 poller，否则 MarkSeen 会打在 nil 上。
	e.server = sync.NewServer(sync.ServerConfig{
		RoomKey:  e.cfg.RoomKey,
		RoomHash: e.cfg.RoomHashValue(),
		NodeID:   e.cfg.DeviceID,
		NodeName: e.cfg.NodeName,
		Version:  Version,
		MaxBytes: e.cfg.ItemMaxBytes,
	}, e.store, e.applyRemote)

	e.bcast = sync.NewBroadcaster(&sync.Client{}, e.peerList)
	// 必须显式注入房间密钥：Broadcaster 未设置时不会发出任何请求（每个 peer 直接报错）。
	e.bcast.SetRoomKey(e.cfg.RoomKey)

	// 中继等额外上行通道：与「是否有 LAN peer」无关，必须独立于下面的 LAN 装配。
	e.pubMu.Lock()
	publishers := append([]func(model.Entry) error(nil), e.publishers...)
	e.pubMu.Unlock()
	_ = publishers

	e.poller = clip.NewPoller(e.board, e.pollInterval(), e.onLocalChange)
	e.poller.Start()

	if !e.cfg.LANEnabled {
		// 只做中继角色（或纯客户端）：不监听局域网端口、不做发现。
		e.mu.Lock()
		e.started = true
		e.mu.Unlock()
		e.logf("[engine] 启动完成：节点 %s（%s），局域网对等已关闭，房间 %s",
			e.cfg.NodeName, shortID(e.cfg.DeviceID), e.cfg.RoomHashValue())
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", e.cfg.Port))
	if err != nil {
		e.poller.Stop()
		return fmt.Errorf("端口 %d 无法监听（可能已被占用），用 --port 或配置里的 port 换一个: %w", e.cfg.Port, err)
	}
	e.ln = ln
	e.httpSrv = &http.Server{Handler: e.server.Handler()}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		if err := e.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			e.setErr(fmt.Errorf("HTTP 服务异常退出: %w", err))
			e.logf("[engine] HTTP 服务异常退出: %v", err)
		}
	}()

	if err := e.startFinder(); err != nil {
		e.logf("[engine] 发现机制启动失败，仅本机可用: %v", err)
		e.setErr(err)
	}

	e.mu.Lock()
	e.started = true
	e.mu.Unlock()

	e.logf("[engine] 启动完成：节点 %s（%s），端口 %d，房间 %s",
		e.cfg.NodeName, shortID(e.cfg.DeviceID), e.cfg.Port, e.cfg.RoomHashValue())
	return nil
}

// Stop 停止轮询、发现与 HTTP 服务。可重复调用。
func (e *Engine) Stop() {
	// 广播告警的节流状态只属于本次运行，Stop 时清空（无 timer/goroutine 需要回收）。
	e.bcMu.Lock()
	e.bcState = nil
	e.bcMu.Unlock()

	e.mu.Lock()
	started := e.started
	e.started = false
	e.mu.Unlock()
	if !started {
		return
	}

	if e.poller != nil {
		e.poller.Stop()
	}
	e.stopFinder()

	if e.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		_ = e.httpSrv.Shutdown(ctx)
		cancel()
	}
	e.wg.Wait()
	e.logf("[engine] 已停止")
}

// SetRelayStatus 由 cmd 层注入中继客户端的地址与连通性判断（供托盘与状态页展示）。
func (e *Engine) SetRelayStatus(url string, enabled bool, connected func() bool) {
	e.relayMu.Lock()
	defer e.relayMu.Unlock()
	e.relayURL = url
	e.relayEnabled = enabled
	e.relayConnected = connected
}

// relaySnapshot 返回中继地址与连通性（未配置时为空/false）。
func (e *Engine) relaySnapshot() (string, bool, bool) {
	e.relayMu.Lock()
	defer e.relayMu.Unlock()
	url := e.relayURL
	enabled := e.relayEnabled
	ok := false
	if e.relayConnected != nil {
		ok = e.relayConnected()
	}
	return url, enabled, ok
}

// Snapshot 返回给托盘/Web UI 的状态快照。
func (e *Engine) Snapshot() Snapshot {
	recent := e.store.List(SnapshotRecent)
	lastErr := e.errText()
	relayURL, relayEnabled, relayOK := e.relaySnapshot()
	return Snapshot{
		LANEnabled:     e.cfg.LANEnabled,
		RelayURL:       relayURL,
		RelayEnabled:   relayEnabled,
		RelayConnected: relayOK,
		NodeName:       e.cfg.NodeName,
		RoomHash:       e.cfg.RoomHashValue(),
		Paused:         e.isPaused(),
		AutoApply:      e.isAutoApply(),
		MDNS:           e.isMDNS(),
		Peers:          len(e.peerList()),
		LastError:      lastErr,
		Recent:         recent,
	}
}

// ListEntries 返回全部本地历史（最新在前）。
func (e *Engine) ListEntries() []model.Entry {
	return e.store.List(0)
}

// PullEntry 取回历史条目：写本机剪贴板 + MarkSeen，不广播（S3）。
//
// 暂停时仍允许取回——这是用户的一次显式动作，不属于自动同步。
func (e *Engine) PullEntry(id string) error {
	entry, ok := e.store.Get(id)
	if !ok {
		return fmt.Errorf("历史里没有 id 为 %q 的条目", id)
	}
	if err := e.applyLocal(entry.Content); err != nil {
		return err
	}
	e.logf("[engine] 取回条目 %s（不广播）", entry.ID)
	return nil
}

// PushCurrent 强制广播当前剪贴板内容一次（S4）。
//
// 暂停时拒绝执行（S5 的「不广播」优先），并返回可展示的错误。
// 内容为空或超限按 S8 跳过；广播失败只记日志不重试（S7）。
func (e *Engine) PushCurrent() error {
	if e.isPaused() {
		return ErrPaused
	}
	content, err := e.board.Read()
	if err != nil {
		return fmt.Errorf("读取剪贴板失败: %w", err)
	}
	content = model.Normalize(content)
	if content == "" {
		return ErrEmptyClipboard
	}
	if e.cfg.ItemMaxBytes > 0 && len(content) > e.cfg.ItemMaxBytes {
		return fmt.Errorf("%w：%d 字节 > %d", ErrTooLarge, len(content), e.cfg.ItemMaxBytes)
	}

	entry := model.NewEntry(e.cfg.DeviceID, e.cfg.NodeName, content, time.Now())
	e.store.Add(entry)
	// 刚复制完就点「发送当前剪贴板」时，轮询器可能还没消费这次变化；
	// 广播后把内容记为已见，紧接着的一轮轮询就不会再产生一次同 ID 广播（L1）。
	if e.poller != nil {
		e.poller.MarkSeen(content)
	}
	e.broadcast(entry)
	return nil
}

// SetPaused 开关同步（S5）。暂停是运行期状态，不落盘。
func (e *Engine) SetPaused(paused bool) {
	e.mu.Lock()
	e.paused = paused
	e.mu.Unlock()
	if paused {
		e.logf("[engine] 已暂停同步：不广播、不应用远端（本地复制仍记入历史）")
	} else {
		e.logf("[engine] 已恢复同步")
	}
}

// SetAutoApply 设置是否把远端条目自动写入本机剪贴板（S2）。
func (e *Engine) SetAutoApply(on bool) error {
	e.mu.Lock()
	e.autoApply = on
	e.mu.Unlock()

	e.cfgMu.Lock()
	e.cfg.AutoApply = on
	err := e.saveConfigLocked()
	e.cfgMu.Unlock()
	if err != nil {
		return err
	}
	e.logf("[engine] 自动应用远端内容：%v", on)
	return nil
}

// SetMDNS 开关 mDNS 发现（design §7），并立即重建发现器；seeds 不受影响。
func (e *Engine) SetMDNS(on bool) error {
	e.mu.Lock()
	e.mdns = on
	e.mu.Unlock()

	e.cfgMu.Lock()
	e.cfg.MDNS = on
	err := e.saveConfigLocked()
	e.cfgMu.Unlock()
	if err != nil {
		return err
	}

	e.mu.RLock()
	started := e.started
	e.mu.RUnlock()
	if started {
		if err := e.startFinder(); err != nil {
			e.setErr(err)
			return err
		}
	}
	e.logf("[engine] mDNS 发现：%v", on)
	return nil
}

// ClearHistory 清空本地历史（不影响剪贴板，也不广播）。
func (e *Engine) ClearHistory() error {
	if err := e.store.Clear(); err != nil {
		return err
	}
	e.logf("[engine] 历史已清空")
	return nil
}

// onLocalChange 是「本机用户复制了新内容」的入口（S1）。
//
// 空内容（图片）与超限内容按 S8 跳过。
func (e *Engine) onLocalChange(content string) {
	content = model.Normalize(content)
	if content == "" {
		// 复制的是图片/二进制：视为无变化，不打日志洪水。
		return
	}
	if e.cfg.ItemMaxBytes > 0 && len(content) > e.cfg.ItemMaxBytes {
		e.logf("[engine] 本机复制的内容 %d 字节超过上限 %d，已跳过（S8）", len(content), e.cfg.ItemMaxBytes)
		return
	}

	entry := model.NewEntry(e.cfg.DeviceID, e.cfg.NodeName, content, time.Now())
	e.store.Add(entry)

	if e.isPaused() {
		// S5：暂停时仍入历史，只是不广播。
		e.logf("[engine] 本机复制 %d 字节（暂停中，不广播）", len(content))
		return
	}
	e.broadcast(entry)
}

// AddPublisher 注册一个额外的上行通道（例如中继客户端）。可多次调用。
func (e *Engine) AddPublisher(fn func(model.Entry) error) {
	if fn == nil {
		return
	}
	e.pubMu.Lock()
	e.publishers = append(e.publishers, fn)
	e.pubMu.Unlock()
}

// publishExtra 把条目交给所有额外通道。每个通道独立 goroutine：
// 中继不可达时不能拖慢剪贴板轮询（S7：失败只记日志，不重试）。
func (e *Engine) publishExtra(entry model.Entry) {
	e.pubMu.Lock()
	pubs := append([]func(model.Entry) error(nil), e.publishers...)
	e.pubMu.Unlock()
	for _, fn := range pubs {
		go func(fn func(model.Entry) error) {
			if err := fn(entry); err != nil {
				e.logf("[engine] 中继上行失败（不重试）：%v", err)
			}
		}(fn)
	}
}

// IngestRemote 把「非局域网入站」的条目并进本机状态（中继客户端用）。
//
// 与局域网入站走完全相同的路径：本地时间排序 → 内容去重 → 只在首次出现时写剪贴板。
// ID 由 origin+content 推导，保证同一内容在多条链路上被识别为同一条（S6）。
func (e *Engine) IngestRemote(entry model.Entry) bool {
	entry.RecvAt = time.Now()
	entry.ID = model.NewEntry(entry.Origin, entry.OriginName, entry.Content, entry.RecvAt).ID
	added := true
	if e.store != nil {
		added = e.store.Add(entry)
	}
	if added {
		e.applyRemote(entry)
	}
	return added
}

// applyRemote 是 sync.Server 的去重回调：只有内容首次出现时才会被调用。
//
// 职责边界：入历史由 sync.Server 完成，这里绝不能再 Add 一次——
// 双写会让同一内容在历史里出现两条。这里只做「写剪贴板 + MarkSeen」。
func (e *Engine) applyRemote(entry model.Entry) {
	if e.isPaused() {
		// S5：暂停时不应用远端。条目已由 sync.Server 记入历史，便于暂停结束后查看。
		e.logf("[engine] 收到远端条目 %s，但同步已暂停（S5）", entry.ID)
		return
	}
	if !e.isAutoApply() {
		e.logf("[engine] 收到远端条目 %s，自动应用已关闭（S2）", entry.ID)
		return
	}
	if err := e.applyLocal(entry.Content); err != nil {
		e.setErr(err)
		e.logf("[engine] 应用远端条目 %s 失败: %v", entry.ID, err)
		return
	}
	e.logf("[engine] 已应用远端条目 %s（来自 %s，%d 字节）", entry.ID, entry.OriginName, len(entry.Content))
}

// applyLocal 写剪贴板 + MarkSeen。S2/S3 共用这条路径，保证「程序写入的内容
// 不会被轮询当成用户复制」这一核心不变量只有一处实现。
func (e *Engine) applyLocal(content string) error {
	if e.poller == nil {
		return ErrNotStarted
	}
	if err := e.board.Write(content); err != nil {
		return fmt.Errorf("写入剪贴板失败: %w", err)
	}
	e.poller.MarkSeen(content)
	return nil
}

// probeDiscovery 触发一次即时的 peer 重探，带 2 秒节流。
//
// 只在「这一轮广播一个节点都没有」时调用：把「对端重启 → 最长 30 秒静默失效」
// 缩短为「下一次复制就能重新连上」。节流是为了避免用户在断网时连续复制导致的
// 探活风暴（每次探活是一次 HTTP 往返）。
func (e *Engine) probeDiscovery() {
	if e.finder == nil {
		return
	}
	now := time.Now().UnixNano()
	last := e.lastProbeNano.Load()
	if now-last < int64(2*time.Second) {
		return
	}
	if !e.lastProbeNano.CompareAndSwap(last, now) {
		return // 已有其它 goroutine 触发，避免探活风暴
	}
	e.finder.ProbeNow()
}

// broadcast 扇出到所有 peers；失败只记日志，不重试（S7）。
func (e *Engine) broadcast(entry model.Entry) {
	// 中继上行必须独立执行：LAN 没有任何 peer 时也要发得出去（这正是跨网络的主路径）。
	e.publishExtra(entry)

	if e.bcast == nil {
		return
	}
	results := e.bcast.Broadcast(entry)
	if len(results) == 0 {
		// 没有任何 peer 时立刻让发现层重探一次：否则对端重启后要等满一个探活周期
		// （默认 30s）才会被重新发现，这段时间用户复制的内容会静默不同步。
		e.probeDiscovery()
		e.logf("[engine] 广播条目 %s：当前无可用节点（已触发一次节点重探）", entry.ID)
		return
	}
	failures := sync.Failures(results)
	e.logf("[engine] 广播条目 %s 到 %d 个节点（失败 %d）", entry.ID, len(results), len(failures))
	for _, r := range failures {
		// S7：只记日志，不重试——剪贴板内容有过期语义，延迟重发会造成时序错乱。
		e.logf("[engine] 广播到 %s 失败（不重试）: %v", r.Peer.Label(), r.Err)
	}
	e.noteBroadcastResults(results)
}

// peerList 把发现结果转成同步层需要的 peer 列表。
func (e *Engine) peerList() []sync.Peer {
	e.finderMu.Lock()
	f := e.finder
	e.finderMu.Unlock()
	if f == nil {
		return nil
	}
	found := f.Peers()
	if len(found) == 0 {
		return nil
	}
	out := make([]sync.Peer, 0, len(found))
	for _, p := range found {
		out = append(out, sync.Peer{NodeID: p.NodeID, Name: p.Name, BaseURL: p.BaseURL})
	}
	return out
}

// startFinder 按当前配置（重新）创建并启动发现器。
//
// 全程持有 finderRebuildMu：并发 SetMDNS 时重建会交错，若不串行化，
// 后一次重建可能把前一次刚启动的 Finder 覆盖掉，旧 Finder 的 goroutine 与 mDNS 连接就泄漏了。
func (e *Engine) startFinder() error {
	e.finderRebuildMu.Lock()
	defer e.finderRebuildMu.Unlock()
	e.stopFinderLocked()

	if !e.isMDNS() && len(e.cfg.Seeds) == 0 {
		return nil
	}
	f := discovery.New(discovery.Config{
		NodeID:   e.cfg.DeviceID,
		NodeName: e.cfg.NodeName,
		RoomHash: e.cfg.RoomHashValue(),
		Version:  Version,
		Port:     e.cfg.Port,
		Seeds:    append([]string(nil), e.cfg.Seeds...),
		MDNS:     e.isMDNS(),
	}, nil)
	f.SetLogger(func(format string, args ...any) {
		e.logf("[discovery] "+format, args...)
	})

	if err := f.Start(); err != nil {
		return err
	}
	e.finderMu.Lock()
	e.finder = f
	e.finderMu.Unlock()
	return nil
}

// stopFinder 停掉并清空发现器（与 startFinder 串行，避免重建交错）。
func (e *Engine) stopFinder() {
	e.finderRebuildMu.Lock()
	defer e.finderRebuildMu.Unlock()
	e.stopFinderLocked()
}

// stopFinderLocked 是 stopFinder 的实现体，调用方必须持有 finderRebuildMu。
func (e *Engine) stopFinderLocked() {
	e.finderMu.Lock()
	f := e.finder
	e.finder = nil
	e.finderMu.Unlock()
	if f != nil {
		f.Stop()
	}
}

// refreshPeers 立即重新探活一次 seeds（供测试与将来的「刷新节点」菜单项使用）。
func (e *Engine) refreshPeers() {
	e.finderMu.Lock()
	f := e.finder
	e.finderMu.Unlock()
	if f != nil {
		f.ProbeNow()
	}
}

func (e *Engine) pollInterval() time.Duration {
	if e.cfg.PollIntervalMs <= 0 {
		return clip.DefaultInterval
	}
	return time.Duration(e.cfg.PollIntervalMs) * time.Millisecond
}

func (e *Engine) isPaused() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.paused
}

func (e *Engine) isAutoApply() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.autoApply
}

func (e *Engine) isMDNS() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.mdns
}

// setErr 记录最后一条可展示的错误。
func (e *Engine) setErr(err error) {
	if err == nil {
		return
	}
	e.mu.Lock()
	e.lastErr = err.Error()
	e.mu.Unlock()
}

// errText 汇总引擎与历史落盘两处的最后错误（历史错误优先，因为它影响持久化）。
func (e *Engine) errText() string {
	if err := e.store.LastError(); err != nil {
		return err.Error()
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.lastErr
}

// saveConfigLocked 把当前配置写回磁盘；调用方必须持有 cfgMu。
//
// json.Marshal 会读取 Config 的每一个字段，因此它和「改字段」必须在同一把锁里，
// 否则并发点两个托盘开关时会出现「落盘读到半个旧值」的数据竞争。
// 配置目录不可用时返回错误但不影响运行。
func (e *Engine) saveConfigLocked() error {
	if e.cfgDir == "" {
		return errors.New("配置目录不可用，改动未保存")
	}
	if err := e.cfg.Save(e.cfgDir); err != nil {
		e.setErr(err)
		return err
	}
	return nil
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

// --- 广播持续失败的可操作提示 -------------------------------------------------
//
// 背景（真实故障）：Windows Defender 防火墙默认阻断**入站**，而 clipshare 是对等 P2P，
// 两个方向都要能连。于是出现「Windows 能发出去、Linux 发不回来」的单向静默同步失败，
// 日志里只有一行「广播失败」，用户不知道该怎么办。这里把「连续失败」翻译成能照做的提示。
//
// 分类原则：区分「连不上」（防火墙/关机/不在同一网段）与「连上了但被拒」（房间码不一致）。
//   - 连不上：HTTP 请求根本没到达对端，net/http 会把拨号失败/超时/主机不可达统一包成
//     实现 net.Error 的 *url.Error（底层是 *net.OpError 包着 ECONNREFUSED/EHOSTUNREACH 等）。
//     因此这里用 net.Error + context 超时判定即可，不需要逐个平台去比 syscall 常量
//     （那些常量在 Windows 上的名字与语义还不完全一样）。
//   - 连上了但被拒：sync.Client 会返回 *sync.PostError，其中 Status 是真实 HTTP 状态码；
//     401/403 正是「房间码不一致」的表现（服务端按 Bearer/roomHash 拒收）。

// failureKind 是广播失败的分类。
type failureKind int

const (
	// failureConnectivity：请求没到达对端（连接被拒、超时、主机不可达）。
	failureConnectivity failureKind = iota
	// failureAuth：对端可达但拒绝鉴权（401/403，多半是房间码不一致）。
	failureAuth
	// failureServer：对端可达但返回了其它错误（5xx 之类），需要看对端日志。
	failureServer
)

// peerFailure 是单个 peer 的连续失败状态。
type peerFailure struct {
	streak   int       // 连续「整轮全部失败」的轮次
	lastWarn time.Time // 上次告警时间，用于节流
}

// isConnectivityErr 判定错误是否属于「连不上」。
//
// 依据：Go 的 net/http 把拨号/读写失败与超时都包成 *url.Error，它实现了 net.Error；
// 未拿到 HTTP 响应的失败（连接被拒、主机不可达、i/o 超时、context 超时）都落在这里。
// 已经拿到响应的错误会走 *sync.PostError 分支，不会进这个函数。
func isConnectivityErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// classifyBroadcastError 给一次广播失败分类。
func classifyBroadcastError(err error) failureKind {
	var pe *sync.PostError
	if errors.As(err, &pe) {
		switch {
		case pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden:
			return failureAuth
		case pe.Status == 0:
			// 没拿到状态码 = 请求没到对端。
			return failureConnectivity
		default:
			return failureServer
		}
	}
	if isConnectivityErr(err) {
		return failureConnectivity
	}
	return failureServer
}

// noteBroadcastResults 汇总一轮广播的结果：
//   - 只要有一个 peer 成功，就说明链路本身是通的，清空所有连续失败计数；
//   - 整轮全部失败时，逐个 peer 累计连续失败轮次，到阈值后发一条节流提示。
func (e *Engine) noteBroadcastResults(results []sync.Result) {
	if len(results) == 0 {
		return
	}
	for _, r := range results {
		if r.OK() {
			e.bcMu.Lock()
			for _, st := range e.bcState {
				st.streak = 0
			}
			e.bcMu.Unlock()
			return
		}
	}

	now := e.now()
	var warnings []string
	e.bcMu.Lock()
	if e.bcState == nil { // Stop 之后被误调用时不要 panic
		e.bcState = map[string]*peerFailure{}
	}
	for _, r := range results {
		key := r.Peer.BaseURL
		st := e.bcState[key]
		if st == nil {
			st = &peerFailure{}
			e.bcState[key] = st
		}
		st.streak++
		if st.streak < broadcastWarnThreshold {
			continue
		}
		if !st.lastWarn.IsZero() && now.Sub(st.lastWarn) < broadcastWarnInterval {
			continue // 节流窗口内不重复刷
		}
		st.lastWarn = now
		warnings = append(warnings, broadcastWarning(r, st.streak))
	}
	e.bcMu.Unlock()

	for _, msg := range warnings {
		e.logf("[engine] %s", msg)
		e.setErr(errors.New(msg))
	}
}

// broadcastWarning 生成一条「照做就行」的提示，端口取自该 peer 的实际地址。
func broadcastWarning(r sync.Result, streak int) string {
	addr, port := peerHostPort(r.Peer.BaseURL)
	switch classifyBroadcastError(r.Err) {
	case failureConnectivity:
		return fmt.Sprintf("节点 %s 连续 %d 轮广播全部失败：连不上对端（%v）。"+
			"若对端是 Windows，请用管理员 PowerShell 放行入站 TCP %s："+
			"New-NetFirewallRule -DisplayName clipshare -Direction Inbound -Protocol TCP -LocalPort %s -Action Allow；"+
			"也可能是机器已关机或不在同一网段。",
			addr, streak, r.Err, port, port)
	case failureAuth:
		return fmt.Sprintf("节点 %s 连续 %d 轮广播被拒（%v）：对端可达但拒绝了鉴权，"+
			"多半是两端的房间码（roomKey）不一致，请核对 --room 或 config.json 里的 roomKey。",
			addr, streak, r.Err)
	default:
		return fmt.Sprintf("节点 %s 连续 %d 轮广播失败（%v）：对端服务可达但返回错误，"+
			"请查看对端日志（端口 %s 是否被别的程序占用）。", addr, streak, r.Err, port)
	}
}

// peerHostPort 从 BaseURL 里取出「host:port」与端口，用于提示文案。
func peerHostPort(baseURL string) (addr, port string) {
	raw := strings.TrimSpace(baseURL)
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		addr = u.Host
		port = u.Port()
	} else {
		addr = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://"), "/")
		if i := strings.LastIndex(addr, ":"); i > 0 {
			port = addr[i+1:]
		}
	}
	if port == "" {
		port = "该"
	}
	return addr, port
}
