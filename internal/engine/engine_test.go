package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"

	"clipshare/internal/config"
	"clipshare/internal/history"
	"clipshare/internal/model"
	"clipshare/internal/sync"
)

// fakeClip 是并发安全的假剪贴板：测试直接 set/get 内容，模拟用户复制。
type fakeClip struct {
	mu      stdsync.Mutex
	content string
	writes  int
	reads   int
}

func (c *fakeClip) Read() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads++
	return c.content, nil
}

// readCount 返回 Read 被调用次数，供测试观察轮询周期相位。
func (c *fakeClip) readCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func (c *fakeClip) Write(s string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.content = s
	c.writes++
	return nil
}

// set 模拟用户在本机复制内容（不经过 engine 的写入路径）。
func (c *fakeClip) set(s string) {
	c.mu.Lock()
	c.content = s
	c.mu.Unlock()
}

func (c *fakeClip) get() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content
}

func (c *fakeClip) writeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

// logBuf 收集 engine 日志，用来断言「有没有发生广播」这类行为。
type logBuf struct {
	mu    stdsync.Mutex
	lines []string
}

func (l *logBuf) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logBuf) count(substr string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, substr) {
			n++
		}
	}
	return n
}

func (l *logBuf) dump() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("取空闲端口失败: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func testConfig(deviceID, name string, port int, roomKey string) *config.Config {
	c := config.Default()
	c.NodeName = name
	c.DeviceID = deviceID
	c.RoomKey = roomKey
	c.Port = port
	c.HistoryLimit = 50
	c.ItemMaxBytes = 1024
	c.PollIntervalMs = 100
	c.AutoApply = true
	c.MDNS = false
	c.Persist = false
	c.Seeds = nil
	return c
}

type harness struct {
	e    *Engine
	clip *fakeClip
	cfg  *config.Config
	logs *logBuf
	t    *testing.T
}

// newHarness 造一台不落盘、不碰用户配置目录的引擎。
//
// 配置目录通过 cfg.Dir 注入（与 main 走 config.Load(dir) 的路径一致），
// 这样也顺带验证 engine.New 确实用的是调用方指定的目录。
func newHarness(t *testing.T, cfg *config.Config) *harness {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	store := history.New(cfg.HistoryLimit, "", false)
	fc := &fakeClip{}
	logs := &logBuf{}
	e := New(cfg, store, fc, logs.logf)
	t.Cleanup(e.Stop)
	return &harness{e: e, clip: fc, cfg: cfg, logs: logs, t: t}
}

// start 启动引擎，并等轮询器建立基线：Poller 启动首轮读到的内容不算「用户复制」，
// 紧接着 set 可能被当成基线而吞掉，因此启动后先等过一个轮询周期再让测试改内容。
func (h *harness) start(t *testing.T) {
	t.Helper()
	if err := h.e.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	waitIdle(3 * time.Duration(h.cfg.PollIntervalMs) * time.Millisecond)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func waitIdle(d time.Duration) { time.Sleep(d) }

func TestSnapshotBeforeStart(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)

	snap := h.e.Snapshot()
	if snap.NodeName != "A" || snap.Paused || !snap.AutoApply || snap.MDNS {
		t.Fatalf("快照初值异常: %+v", snap)
	}
	if snap.RoomHash != model.RoomHash("room-key-123") {
		t.Fatalf("RoomHash 异常: %q", snap.RoomHash)
	}
	if snap.Peers != 0 || len(snap.Recent) != 0 {
		t.Fatalf("未启动时不应有 peer/历史: %+v", snap)
	}
}

func TestStartTwiceAndStopIdempotent(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)
	if err := h.e.Start(); !errors.Is(err, ErrAlreadyStarted) {
		t.Fatalf("重复 Start 应返回 ErrAlreadyStarted，得到 %v", err)
	}
	h.e.Stop()
	h.e.Stop() // 重复 Stop 必须安全
}

func TestStartPortInUse(t *testing.T) {
	port := freePort(t)
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("占位监听失败: %v", err)
	}
	defer ln.Close()

	cfg := testConfig("devA", "A", port, "room-key-123")
	h := newHarness(t, cfg)
	err = h.e.Start()
	if err == nil {
		t.Fatal("端口被占用时 Start 必须报错")
	}
	if !strings.Contains(err.Error(), "端口") {
		t.Fatalf("错误信息应提示改端口，得到 %v", err)
	}
}

func TestPushCurrentEmpty(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)
	if err := h.e.PushCurrent(); !errors.Is(err, ErrEmptyClipboard) {
		t.Fatalf("空剪贴板应返回 ErrEmptyClipboard，得到 %v", err)
	}
	if got := len(h.e.ListEntries()); got != 0 {
		t.Fatalf("空内容不应入历史，得到 %d 条", got)
	}
}

func TestPushCurrentTooLarge(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)
	h.clip.set(strings.Repeat("x", cfg.ItemMaxBytes+1))
	if err := h.e.PushCurrent(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("超限应返回 ErrTooLarge，得到 %v", err)
	}
}

func TestPushCurrentPaused(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)
	h.e.SetPaused(true)
	h.clip.set("hello")
	if err := h.e.PushCurrent(); !errors.Is(err, ErrPaused) {
		t.Fatalf("暂停时强制广播应返回 ErrPaused，得到 %v", err)
	}
}

// S1：本机复制入历史；没有 peer 时广播退化为记日志。
func TestS1LocalCopyEntersHistory(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)

	h.clip.set("本机复制")
	waitFor(t, "本机复制入历史", func() bool { return len(h.e.ListEntries()) == 1 })

	entry := h.e.ListEntries()[0]
	if entry.Content != "本机复制" || entry.Origin != "devA" || entry.OriginName != "A" {
		t.Fatalf("条目异常: %+v", entry)
	}
	if h.e.Snapshot().Peers != 0 {
		t.Fatal("没有发现配置时不应有 peer")
	}
}

// S8：空内容与超限内容不入历史、不广播。
func TestS8SkipsEmptyAndOversize(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)

	h.clip.set("")
	waitIdle(250 * time.Millisecond)
	if got := len(h.e.ListEntries()); got != 0 {
		t.Fatalf("空内容不应入历史，得到 %d 条", got)
	}

	h.clip.set(strings.Repeat("y", cfg.ItemMaxBytes+10))
	waitFor(t, "超限日志", func() bool { return h.logs.count("超过上限") > 0 })
	if got := len(h.e.ListEntries()); got != 0 {
		t.Fatalf("超限内容不应入历史，得到 %d 条", got)
	}
}

// S5：暂停时本地复制仍入历史，但不广播。
func TestS5PausedStillRecordsLocalCopy(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)
	h.e.SetPaused(true)

	h.clip.set("暂停中的复制")
	waitFor(t, "暂停中仍入历史", func() bool { return len(h.e.ListEntries()) == 1 })
	waitIdle(200 * time.Millisecond)
	if h.logs.count("暂停中，不广播") == 0 {
		t.Fatalf("应记录「暂停中不广播」，日志:\n%s", h.logs.dump())
	}

	if !h.e.Snapshot().Paused {
		t.Fatal("Snapshot.Paused 应为 true")
	}
}

func TestClearHistory(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)
	h.clip.set("待清空")
	waitFor(t, "入历史", func() bool { return len(h.e.ListEntries()) == 1 })

	if err := h.e.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory 失败: %v", err)
	}
	if got := len(h.e.ListEntries()); got != 0 {
		t.Fatalf("清空后应为 0 条，得到 %d", got)
	}
	// 剪贴板内容不受影响。
	if got := h.clip.get(); got != "待清空" {
		t.Fatalf("清空历史不应改剪贴板，得到 %q", got)
	}
}

func TestSetAutoApplyPersists(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	if err := h.e.SetAutoApply(false); err != nil {
		t.Fatalf("SetAutoApply 失败: %v", err)
	}
	if h.e.Snapshot().AutoApply {
		t.Fatal("Snapshot.AutoApply 应为 false")
	}
	if cfg.AutoApply {
		t.Fatal("配置对象应同步更新")
	}
	if _, err := config.Load(h.e.cfgDir); !errors.Is(err, nil) {
		t.Fatalf("配置应已落盘: %v", err)
	}
}

func TestSetMDNSRebuildsFinder(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	cfg.MDNS = false
	h := newHarness(t, cfg)
	h.start(t)
	if err := h.e.SetMDNS(true); err != nil {
		t.Fatalf("SetMDNS(true) 失败: %v", err)
	}
	if !h.e.Snapshot().MDNS {
		t.Fatal("Snapshot.MDNS 应为 true")
	}
	if err := h.e.SetMDNS(false); err != nil {
		t.Fatalf("SetMDNS(false) 失败: %v", err)
	}
	if h.e.Snapshot().MDNS {
		t.Fatal("Snapshot.MDNS 应为 false")
	}
}

func TestPullEntryUnknownID(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)
	if err := h.e.PullEntry("nope"); err == nil {
		t.Fatal("未知 id 取回必须报错")
	}
}

// Version 必须是可注入的：main 用 ldflags 注入的版本覆盖它之后，
// /v1/health 上报的版本要跟着变（否则 --version 与对端看到的版本会不一致）。
func TestVersionInjectionReportedByHealth(t *testing.T) {
	old := Version
	Version = "9.9.9-test"
	t.Cleanup(func() { Version = old })

	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t)

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/health", cfg.Port))
	if err != nil {
		t.Fatalf("请求 /v1/health 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/v1/health 应返回 200，得到 %d", resp.StatusCode)
	}

	var got struct {
		NodeID   string `json:"nodeId"`
		Name     string `json:"name"`
		Version  string `json:"version"`
		RoomHash string `json:"roomHash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("解析 /v1/health 响应失败: %v", err)
	}
	if got.Version != "9.9.9-test" {
		t.Fatalf("/v1/health 上报版本应为注入值 9.9.9-test，得到 %q", got.Version)
	}
	if got.NodeID != "devA" || got.Name != "A" {
		t.Fatalf("health 节点信息异常: %+v", got)
	}
	if got.RoomHash != model.RoomHash("room-key-123") {
		t.Fatalf("health roomHash 异常: %q", got.RoomHash)
	}
}

// 托盘开关必须写回「调用方传进来的配置目录」（即 --config-dir），
// 而不是 engine 自己推算的默认目录——否则带 --config-dir 运行时会在默认目录
// 凭空多出一份 config.json，双进程实测还会互相覆盖。
func TestSetAutoApplyWritesToInjectedConfigDir(t *testing.T) {
	injected := t.TempDir()
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	cfg.Dir = injected

	defaultDir, dirErr := config.Dir()
	if dirErr == nil {
		// 记录默认目录的现状，测试结束前必须保持原样（不被创建、不被改写）。
		before, beforeExists := readIfExists(t, config.Path(defaultDir))
		t.Cleanup(func() {
			after, afterExists := readIfExists(t, config.Path(defaultDir))
			if beforeExists != afterExists || string(before) != string(after) {
				t.Errorf("默认配置目录被写入了：%s（exists %v → %v）", config.Path(defaultDir), beforeExists, afterExists)
			}
		})
	}

	h := newHarness(t, cfg)
	if h.e.cfgDir != injected {
		t.Fatalf("engine 应使用注入的配置目录 %q，实际 %q", injected, h.e.cfgDir)
	}

	want := !cfg.AutoApply
	if err := h.e.SetAutoApply(want); err != nil {
		t.Fatalf("SetAutoApply: %v", err)
	}

	raw, exists := readIfExists(t, config.Path(injected))
	if !exists {
		t.Fatalf("注入的配置目录里应出现 config.json")
	}
	var saved config.Config
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("解析写回的配置: %v", err)
	}
	if saved.AutoApply != want {
		t.Fatalf("写回的 autoApply = %v，期望 %v", saved.AutoApply, want)
	}
}

// cfg.Dir 为空时回退到默认目录（保住直接构造 &config.Config{} 的旧用法）。
func TestConfigDirFallbackWhenUnset(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	cfg.Dir = "" // 故意不注入

	store := history.New(cfg.HistoryLimit, "", false)
	e := New(cfg, store, &fakeClip{}, nil)
	t.Cleanup(e.Stop)

	want, err := config.Dir()
	if err != nil {
		t.Skipf("当前环境无法确定默认配置目录: %v", err)
	}
	if e.cfgDir != want {
		t.Fatalf("cfg.Dir 为空时应回退到 %q，实际 %q", want, e.cfgDir)
	}
}

// readIfExists 读文件内容并报告是否存在；不存在时返回 (nil,false)。
func readIfExists(t *testing.T, path string) ([]byte, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return raw, true
}

// TestConcurrentConfigToggles 复现托盘并发点两个开关的场景：
// tray 每次点击都是 go func()，用户快速点「自动应用」和「mDNS」时，
// SetAutoApply / SetMDNS 会并发修改共享的 cfg，而 saveConfig 正在 json.Marshal 它。
func TestConcurrentConfigToggles(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)

	var wg stdsync.WaitGroup
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			if err := h.e.SetAutoApply(i%2 == 0); err != nil {
				t.Errorf("SetAutoApply: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 100; i++ {
			if err := h.e.SetMDNS(i%2 == 0); err != nil {
				t.Errorf("SetMDNS: %v", err)
				return
			}
		}
	}()
	close(start)
	wg.Wait()

	// 最后一个写入值必须完整落盘（不能是并发写坏的中间态）。
	raw, ok := readIfExists(t, config.Path(cfg.Dir))
	if !ok {
		t.Fatal("并发开关后配置应已落盘")
	}
	var saved config.Config
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("并发写回的配置不是合法 JSON: %v\n%s", err, raw)
	}
	if saved.AutoApply != h.e.Snapshot().AutoApply || saved.MDNS != h.e.Snapshot().MDNS {
		t.Fatalf("落盘值与内存状态不一致: 落盘 autoApply=%v mdns=%v，内存 %+v",
			saved.AutoApply, saved.MDNS, h.e.Snapshot())
	}
}

// L1：刚复制完就点「发送当前剪贴板」时，轮询器可能还没消费这次变化。
// PushCurrent 广播后必须把内容记为已见，否则紧接着的一轮轮询会再广播一次同 ID 条目。
func TestPushCurrentSuppressesPendingPoll(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.start(t) // 基线已建立，且没有 peer（广播退化为记日志，便于计数）

	// 等到「刚完成一轮轮询」的时刻再动手：此时离下一次轮询还有一整个周期，
	// 复制与 PushCurrent 都不会与轮询交错，避免测试偶发（轮询半路检测到变化属正常 S1）。
	interval := 3 * time.Duration(cfg.PollIntervalMs) * time.Millisecond
	reads := h.clip.readCount()
	waitFor(t, "轮询器完成一轮读取", func() bool { return h.clip.readCount() > reads })

	h.clip.set("刚复制就推送")
	if err := h.e.PushCurrent(); err != nil {
		t.Fatalf("PushCurrent: %v", err)
	}
	after := h.logs.count("广播条目")
	if after != 1 {
		t.Fatalf("PushCurrent 应恰好广播一次，得到 %d 次\n日志:\n%s", after, h.logs.dump())
	}

	// 跨过至少两个轮询周期：若 PushCurrent 没把内容记为已见，
	// 下一轮轮询会发现「剪贴板变了」并再广播一次同 ID 条目。
	waitIdle(2 * interval)
	if n := h.logs.count("广播条目"); n != after {
		t.Fatalf("PushCurrent 后不应再因轮询重复广播，广播次数 %d → %d\n日志:\n%s",
			after, n, h.logs.dump())
	}
	if got := len(h.e.ListEntries()); got != 1 {
		t.Fatalf("同一条内容只应入历史一次，得到 %d 条", got)
	}
}

// 并发 SetMDNS 时必须串行化 Finder 重建：不能交错到「旧 Finder 还没停就被覆盖」。
// 这里用 seeds（而不是真 mDNS）驱动重建，避免依赖多播环境；断言不 panic、不死锁、
// 最终状态与最后一次调用一致，且 Stop 能干净收尾。
func TestConcurrentSetMDNSRebuildSerialized(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	cfg.MDNS = false
	cfg.Seeds = []string{fmt.Sprintf("127.0.0.1:%d", freePort(t))} // 无对端，探测快速失败

	h := newHarness(t, cfg)
	h.start(t)

	var wg stdsync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < 2; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 8; i++ {
				on := (i+w)%2 == 0
				if err := h.e.SetMDNS(on); err != nil {
					t.Errorf("SetMDNS(%v): %v", on, err)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	// 收敛到确定的终态：seeds 仍在，因此 Finder 必须存在且是最后一个。
	if err := h.e.SetMDNS(false); err != nil {
		t.Fatalf("收尾 SetMDNS(false): %v", err)
	}
	waitFor(t, "重建后 Finder 仍存在（seeds 非空）", func() bool {
		h.e.finderMu.Lock()
		defer h.e.finderMu.Unlock()
		return h.e.finder != nil
	})
	if snap := h.e.Snapshot(); snap.MDNS {
		t.Fatalf("终态 mDNS 应为 false，得到 %+v", snap)
	}

	done := make(chan struct{})
	go func() { h.e.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("并发重建后 Stop 卡住（Finder 泄漏或死锁）")
	}
}

// usePeers 用假 peer 列表替换广播器（不依赖 discovery 与真实网络）。
func (h *harness) usePeers(peers ...sync.Peer) {
	h.t.Helper()
	b := sync.NewBroadcaster(&sync.Client{Timeout: 500 * time.Millisecond}, func() []sync.Peer {
		out := make([]sync.Peer, len(peers))
		copy(out, peers)
		return out
	})
	b.SetRoomKey(h.cfg.RoomKey)
	h.e.bcast = b
}

// 连接层持续失败：未达阈值不提示，达阈值提示一次并带上真实端口，节流窗口内不重复。
func TestBroadcastConnectivityWarning(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.t = t

	deadPort := freePort(t) // 没人监听，连接会立刻被拒
	addr := fmt.Sprintf("127.0.0.1:%d", deadPort)
	h.usePeers(sync.Peer{NodeID: "win1", Name: "WIN10", BaseURL: "http://" + addr})

	clock := time.Unix(1700000000, 0)
	h.e.now = func() time.Time { return clock }

	entry := model.NewEntry("devA", "A", "触发广播", time.Now())
	for i := 1; i < broadcastWarnThreshold; i++ {
		h.e.broadcast(entry)
	}
	if n := h.logs.count("New-NetFirewallRule"); n != 0 {
		t.Fatalf("未达阈值（%d 轮）不应提示防火墙，实际 %d 条\n日志:\n%s", broadcastWarnThreshold-1, n, h.logs.dump())
	}

	h.e.broadcast(entry) // 第 3 轮
	if n := h.logs.count("New-NetFirewallRule"); n != 1 {
		t.Fatalf("连续 %d 轮全部失败后应提示一次防火墙，实际 %d 条\n日志:\n%s",
			broadcastWarnThreshold, n, h.logs.dump())
	}
	if n := h.logs.count(addr); n == 0 {
		t.Fatalf("提示里应包含该 peer 的真实地址 %q\n日志:\n%s", addr, h.logs.dump())
	}
	if n := h.logs.count(fmt.Sprintf("-LocalPort %d", deadPort)); n == 0 {
		t.Fatalf("提示里应包含该 peer 的真实端口 %d\n日志:\n%s", deadPort, h.logs.dump())
	}

	// 节流窗口内继续失败：不重复提示。
	h.e.broadcast(entry)
	if n := h.logs.count("New-NetFirewallRule"); n != 1 {
		t.Fatalf("节流窗口内不应重复提示，实际 %d 条\n日志:\n%s", n, h.logs.dump())
	}

	// 过了窗口再次失败：可以再提示一次。
	clock = clock.Add(broadcastWarnInterval + time.Second)
	h.e.broadcast(entry)
	if n := h.logs.count("New-NetFirewallRule"); n != 2 {
		t.Fatalf("节流窗口过后应再次提示，实际 %d 条\n日志:\n%s", n, h.logs.dump())
	}
}

// 对端可达但返回 401：提示应是「房间码不一致」，不能混成防火墙问题。
func TestBroadcastAuthWarningIsDifferentFromFirewall(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.t = t

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	h.usePeers(sync.Peer{NodeID: "roomy", Name: "OTHER-ROOM", BaseURL: srv.URL})
	entry := model.NewEntry("devA", "A", "触发广播", time.Now())
	for i := 0; i < broadcastWarnThreshold; i++ {
		h.e.broadcast(entry)
	}

	if n := h.logs.count("房间码"); n != 1 {
		t.Fatalf("401 持续失败应提示房间码不一致一次，实际 %d 条\n日志:\n%s", n, h.logs.dump())
	}
	if n := h.logs.count("New-NetFirewallRule"); n != 0 {
		t.Fatalf("鉴权失败不能提示防火墙，实际 %d 条\n日志:\n%s", n, h.logs.dump())
	}
}

// 只要有一轮成功，连续失败计数必须清零（不要在下一次偶发失败时借旧账触发提示）。
func TestBroadcastWarningStreakResetsOnSuccess(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.t = t

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer good.Close()
	deadPort := freePort(t)

	var useDead atomic.Bool
	b := sync.NewBroadcaster(&sync.Client{Timeout: 500 * time.Millisecond}, func() []sync.Peer {
		base := good.URL
		if useDead.Load() {
			base = fmt.Sprintf("http://127.0.0.1:%d", deadPort)
		}
		return []sync.Peer{{NodeID: "p1", Name: "P1", BaseURL: base}}
	})
	b.SetRoomKey(cfg.RoomKey)
	h.e.bcast = b

	entry := model.NewEntry("devA", "A", "触发广播", time.Now())
	useDead.Store(false)
	h.e.broadcast(entry) // 成功，重置

	useDead.Store(true)
	for i := 0; i < broadcastWarnThreshold-1; i++ {
		h.e.broadcast(entry)
	}
	if n := h.logs.count("New-NetFirewallRule"); n != 0 {
		t.Fatalf("成功之后重新计数，未达阈值不应提示，实际 %d 条\n日志:\n%s", n, h.logs.dump())
	}
	h.e.broadcast(entry) // 第 3 次连续失败
	if n := h.logs.count("New-NetFirewallRule"); n != 1 {
		t.Fatalf("达阈值应提示一次，实际 %d 条\n日志:\n%s", n, h.logs.dump())
	}
}

// 节流状态随 Stop 清空（没有 timer/goroutine 需要回收，但状态不能跨运行残留）。
func TestStopClearsBroadcastWarningState(t *testing.T) {
	cfg := testConfig("devA", "A", freePort(t), "room-key-123")
	h := newHarness(t, cfg)
	h.t = t
	h.start(t)

	deadPort := freePort(t)
	h.usePeers(sync.Peer{NodeID: "p1", Name: "P1", BaseURL: fmt.Sprintf("http://127.0.0.1:%d", deadPort)})
	entry := model.NewEntry("devA", "A", "触发广播", time.Now())
	for i := 0; i < broadcastWarnThreshold; i++ {
		h.e.broadcast(entry)
	}

	h.e.Stop()
	h.e.bcMu.Lock()
	left := len(h.e.bcState)
	h.e.bcMu.Unlock()
	if left != 0 {
		t.Fatalf("Stop 后广播告警状态应清空，仍有 %d 条", left)
	}
}
