package clip

import (
	"errors"
	stdsync "sync"
	"testing"
	"time"
)

var errReadFailed = errors.New("剪贴板被占用")

// fakeClipboard 是可注入读错误的假剪贴板。
type fakeClipboard struct {
	mu        stdsync.Mutex
	content   string
	readErr   error
	readCalls int
}

func (f *fakeClipboard) Read() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCalls++
	if f.readErr != nil {
		return "", f.readErr
	}
	return f.content, nil
}

func (f *fakeClipboard) Write(s string) error {
	f.set(s)
	return nil
}

func (f *fakeClipboard) set(s string) {
	f.mu.Lock()
	f.content = s
	f.mu.Unlock()
}

func (f *fakeClipboard) setErr(err error) {
	f.mu.Lock()
	f.readErr = err
	f.mu.Unlock()
}

func (f *fakeClipboard) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readCalls
}

const testInterval = 5 * time.Millisecond

// startPoller 启动轮询、等首次读完成（基线已建立）后返回回调 channel。
//
// 必须等基线：Start 是异步的，若测试在首次读之前就改了剪贴板，
// 新内容会被当成基线而永远不触发回调。
func startPoller(t *testing.T, fake *fakeClipboard) (*Poller, <-chan string) {
	t.Helper()
	events := make(chan string, 16)
	p := NewPoller(fake, testInterval, func(s string) { events <- s })
	p.Start()
	t.Cleanup(p.Stop)
	waitPrimed(t, fake)
	return p, events
}

func waitPrimed(t *testing.T, fake *fakeClipboard) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for fake.calls() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("轮询线程没有完成首次读取")
		}
		time.Sleep(time.Millisecond)
	}
}

func waitEvent(t *testing.T, events <-chan string) (string, bool) {
	t.Helper()
	select {
	case s := <-events:
		return s, true
	case <-time.After(2 * time.Second):
		return "", false
	}
}

func expectEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	got, ok := waitEvent(t, events)
	if !ok {
		t.Fatalf("期望收到 %q 的变更回调，2s 内没有任何回调", want)
	}
	if got != want {
		t.Fatalf("回调内容 = %q，期望 %q", got, want)
	}
}

func expectNoEvent(t *testing.T, events <-chan string, d time.Duration) {
	t.Helper()
	// 先排空可能已到达的事件，再观察窗口内是否还有新事件。
	deadline := time.After(d)
	for {
		select {
		case s := <-events:
			t.Fatalf("不期望回调，却收到了 %q", s)
		case <-deadline:
			return
		}
	}
}

// TestPollerBaselineNoEvent：启动前就在剪贴板里的内容不算用户复制，不触发回调。
func TestPollerBaselineNoEvent(t *testing.T) {
	fake := &fakeClipboard{content: "启动前的内容"}
	_, events := startPoller(t, fake)
	expectNoEvent(t, events, 60*time.Millisecond)
}

// TestPollerNotifiesExternalChange：外部（用户）复制新内容触发一次回调。
func TestPollerNotifiesExternalChange(t *testing.T) {
	fake := &fakeClipboard{}
	_, events := startPoller(t, fake)

	fake.set("hello")
	expectEvent(t, events, "hello")

	// 同一内容保持不变时不应重复回调。
	expectNoEvent(t, events, 40*time.Millisecond)

	fake.set("world")
	expectEvent(t, events, "world")
}

// TestPollerMarkSeenSuppressesOwnWrite：程序自己写入后 MarkSeen，轮询不回调；
// 但用户之后复制别的内容仍要正常触发（证明没有「吞掉后续变更」）。
func TestPollerMarkSeenSuppressesOwnWrite(t *testing.T) {
	fake := &fakeClipboard{}
	p, events := startPoller(t, fake)

	fake.set("from-remote")
	p.MarkSeen("from-remote")
	expectNoEvent(t, events, 60*time.Millisecond)

	fake.set("user-copy")
	expectEvent(t, events, "user-copy")
}

// TestPollerMarkSeenNormalizes：MarkSeen 和轮询用同一套归一化，
// 否则 \r\n 与 \n 的差异会立刻误触发一次回环广播。
func TestPollerMarkSeenNormalizes(t *testing.T) {
	fake := &fakeClipboard{}
	p, events := startPoller(t, fake)

	fake.set("a\nb")
	p.MarkSeen("a\r\nb")
	expectNoEvent(t, events, 60*time.Millisecond)
}

// TestPollerChangeToEmptyFires：剪贴板变成「没有文本」（复制图片）也是一次变化，
// Poller 照常回调空串；是否采用由调用方判断（spec §8）。
func TestPollerChangeToEmptyFires(t *testing.T) {
	fake := &fakeClipboard{}
	_, events := startPoller(t, fake)

	fake.set("text")
	expectEvent(t, events, "text")

	fake.set("")
	expectEvent(t, events, "")
}

// TestPollerReadErrorSkipped：读失败跳过本轮、保留基线，
// 恢复后仍能正确发现变化。
func TestPollerReadErrorSkipped(t *testing.T) {
	fake := &fakeClipboard{content: "base"}
	_, events := startPoller(t, fake)

	fake.setErr(errReadFailed)
	fake.set("while-error")
	expectNoEvent(t, events, 60*time.Millisecond)

	fake.setErr(nil)
	expectEvent(t, events, "while-error")
}

// TestPollerStopStopsCallbacks：Stop 之后不再有任何回调。
func TestPollerStopStopsCallbacks(t *testing.T) {
	fake := &fakeClipboard{}
	events := make(chan string, 16)
	p := NewPoller(fake, testInterval, func(s string) { events <- s })
	p.Start()
	p.Stop()
	p.Stop() // 幂等

	fake.set("after-stop")
	expectNoEvent(t, events, 60*time.Millisecond)
}

// TestPollerStopBeforeStart：未启动就 Stop 不能挂死。
func TestPollerStopBeforeStart(t *testing.T) {
	p := NewPoller(&fakeClipboard{}, testInterval, nil)
	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop 在 Start 之前调用时阻塞了")
	}
}

// TestPollerMarkSeenFromCallbackNoDeadlock：engine 的 apply 会在回调里写剪贴板
// 并 MarkSeen，此时 Poller 不能持有锁，否则死锁。
func TestPollerMarkSeenFromCallbackNoDeadlock(t *testing.T) {
	fake := &fakeClipboard{}
	var p *Poller
	got := make(chan string, 4)
	p = NewPoller(fake, testInterval, func(s string) {
		got <- s
		p.MarkSeen(s) // 模拟 engine：收到变化后写回并抑制自身写入
	})
	p.Start()
	defer p.Stop()
	waitPrimed(t, fake)

	fake.set("remote-content")
	select {
	case s := <-got:
		if s != "remote-content" {
			t.Fatalf("回调内容 = %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("回调死锁或未触发")
	}

	// 抑制生效：同一内容不再回调。
	expectNoEvent(t, got, 60*time.Millisecond)

	// 轮询线程仍然活着：换内容还能收到。
	fake.set("next")
	select {
	case s := <-got:
		if s != "next" {
			t.Fatalf("回调内容 = %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("MarkSeen 之后轮询线程似乎不再回调")
	}
}

// TestPollerDefaultInterval：interval <= 0 回落到 400ms。
func TestPollerDefaultInterval(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		if got := NewPoller(&fakeClipboard{}, d, nil).interval; got != DefaultInterval {
			t.Fatalf("interval=%v 时得到 %v，期望 %v", d, got, DefaultInterval)
		}
	}
	if got := NewPoller(&fakeClipboard{}, time.Second, nil).interval; got != time.Second {
		t.Fatalf("显式 interval 被改写为 %v", got)
	}
}

// TestPollerNilOnChange：没有回调时不 panic，仍持续轮询。
func TestPollerNilOnChange(t *testing.T) {
	fake := &fakeClipboard{}
	p := NewPoller(fake, testInterval, nil)
	p.Start()
	defer p.Stop()

	fake.set("something")
	deadline := time.Now().Add(time.Second)
	for fake.calls() < 3 && time.Now().Before(deadline) {
		time.Sleep(testInterval)
	}
	if fake.calls() < 3 {
		t.Fatalf("轮询没有持续进行，Read 调用次数 = %d", fake.calls())
	}
}
