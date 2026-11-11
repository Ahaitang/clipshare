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
	stdsync "sync"
	"time"

	"clipshare/internal/clip"
	"clipshare/internal/config"
	"clipshare/internal/discovery"
	"clipshare/internal/history"
	"clipshare/internal/model"
	"clipshare/internal/sync"
)

const (
	// Version 是协议握手与 /v1/health 上报的版本号。
	Version = "0.1.0"

	// SnapshotRecent 是状态快照里携带的最近条目数（托盘菜单直接渲染这些）。
	SnapshotRecent = 10

	// shutdownTimeout 是 Stop 时等待 HTTP 连接收尾的上限。
	shutdownTimeout = 3 * time.Second
)

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

	finderMu stdsync.Mutex
	finder   *discovery.Finder

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
	dir, err := config.Dir()
	e := &Engine{
		cfg:       cfg,
		store:     store,
		board:     board,
		logf:      logf,
		cfgDir:    dir,
		autoApply: cfg.AutoApply,
		mdns:      cfg.MDNS,
	}
	if err != nil {
		// 拿不到配置目录不影响运行，只影响「设置能否落盘」。
		e.cfgDir = ""
		e.setErr(fmt.Errorf("配置目录不可用，设置改动无法保存: %w", err))
	}
	return e
}

// Start 启动 HTTP 服务端、发现、剪贴板轮询。
//
// 端口被占用时返回带处置建议的错误（design §10）。
func (e *Engine) Start() error {
	if err := e.cfg.Validate(); err != nil {
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

	e.poller = clip.NewPoller(e.board, e.pollInterval(), e.onLocalChange)
	e.poller.Start()

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

// Snapshot 返回给托盘/Web UI 的状态快照。
func (e *Engine) Snapshot() Snapshot {
	recent := e.store.List(SnapshotRecent)
	lastErr := e.errText()
	return Snapshot{
		NodeName:  e.cfg.NodeName,
		RoomHash:  e.cfg.RoomHashValue(),
		Paused:    e.isPaused(),
		AutoApply: e.isAutoApply(),
		MDNS:      e.isMDNS(),
		Peers:     len(e.peerList()),
		LastError: lastErr,
		Recent:    recent,
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
	e.cfg.AutoApply = on
	if err := e.saveConfig(); err != nil {
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
	e.cfg.MDNS = on
	if err := e.saveConfig(); err != nil {
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

// broadcast 扇出到所有 peers；失败只记日志，不重试（S7）。
func (e *Engine) broadcast(entry model.Entry) {
	if e.bcast == nil {
		return
	}
	peers := e.peerList()
	if len(peers) == 0 {
		e.logf("[engine] 广播条目 %s：当前无可用节点", entry.ID)
		return
	}
	results := e.bcast.Broadcast(entry)
	failures := sync.Failures(results)
	e.logf("[engine] 广播条目 %s 到 %d 个节点（失败 %d）", entry.ID, len(results), len(failures))
	for _, r := range failures {
		// S7：只记日志，不重试——剪贴板内容有过期语义，延迟重发会造成时序错乱。
		e.logf("[engine] 广播到 %s 失败（不重试）: %v", r.Peer.Label(), r.Err)
	}
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
func (e *Engine) startFinder() error {
	e.stopFinder()

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

// stopFinder 停掉并清空发现器。
func (e *Engine) stopFinder() {
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

// saveConfig 把当前配置写回磁盘；配置目录不可用时返回错误但不影响运行。
func (e *Engine) saveConfig() error {
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
