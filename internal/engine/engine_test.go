package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"clipshare/internal/config"
	"clipshare/internal/history"
	"clipshare/internal/model"
)

// fakeClip 是并发安全的假剪贴板：测试直接 set/get 内容，模拟用户复制。
type fakeClip struct {
	mu      stdsync.Mutex
	content string
	writes  int
}

func (c *fakeClip) Read() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content, nil
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
	return &harness{e: e, clip: fc, cfg: cfg, logs: logs}
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
