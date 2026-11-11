package engine

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"clipshare/internal/model"
)

// newPair 造两台互指的引擎：同一房间、各自独立端口，seeds 指向对方。
// mDNS 关闭，这条路径同时验证「禁多播网络靠 seeds 也能发现」。
func newPair(t *testing.T) (a, b *harness) {
	t.Helper()
	const roomKey = "shared-room-key-1"
	portA, portB := freePort(t), freePort(t)

	cfgA := testConfig("devA", "A", portA, roomKey)
	cfgA.Seeds = []string{fmt.Sprintf("127.0.0.1:%d", portB)}
	cfgB := testConfig("devB", "B", portB, roomKey)
	cfgB.Seeds = []string{fmt.Sprintf("127.0.0.1:%d", portA)}

	a = newHarness(t, cfgA)
	b = newHarness(t, cfgB)

	a.start(t)
	b.start(t)

	// Start 时对端可能还没监听，这里显式立即探活一次（等价于 30s 周期提前触发）。
	a.e.refreshPeers()
	b.e.refreshPeers()
	waitPeers(t, a, 1)
	waitPeers(t, b, 1)
	return a, b
}

func waitPeers(t *testing.T, h *harness, want int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%s 发现 %d 个节点（当前 %d）", h.cfg.NodeName, want, h.e.Snapshot().Peers), func() bool {
		return h.e.Snapshot().Peers == want
	})
}

func waitClip(t *testing.T, h *harness, want string) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%s 的剪贴板变成 %q（当前 %q）", h.cfg.NodeName, want, h.clip.get()), func() bool {
		return h.clip.get() == want
	})
}

func waitStable(d time.Duration) { time.Sleep(d) }

func findEntry(entries []model.Entry, content string) (model.Entry, bool) {
	for _, e := range entries {
		if e.Content == content {
			return e, true
		}
	}
	return model.Entry{}, false
}

// 核心集成测：A 复制 → B 应用 → A→B→A 不回环。
func TestIntegrationNoLoopback(t *testing.T) {
	a, b := newPair(t)

	const content = "来自 A 的第一条"
	a.clip.set(content)

	// S2：B 的剪贴板被写入，历史里有 A 的条目（且只有一条——双写会让它变成两条）。
	waitClip(t, b, content)
	entries := b.e.ListEntries()
	if len(entries) != 1 {
		t.Fatalf("B 应只有 1 条历史，得到 %d 条: %+v", len(entries), entries)
	}
	if entries[0].Origin != "devA" || entries[0].OriginName != "A" {
		t.Fatalf("B 收到的条目来源不对: %+v", entries[0])
	}

	// 核心不变量：B 应用远端内容后不得回播。
	if n := b.logs.count("广播条目"); n != 0 {
		t.Fatalf("B 不应广播任何条目，实际广播 %d 次\n日志:\n%s", n, b.logs.dump())
	}

	// 等足够久（多个轮询周期），确认 A 没有收到任何入站请求：
	// 若 B 回播，A 要么记一条新历史（内容不同），要么因去重刷新 RecvAt（内容相同）。
	aEntry, ok := findEntry(a.e.ListEntries(), content)
	if !ok {
		t.Fatalf("A 的历史里应有自己复制的内容")
	}
	waitStable(600 * time.Millisecond)

	if n := len(a.e.ListEntries()); n != 1 {
		t.Fatalf("A 的历史条数不应变化，得到 %d 条: %+v", n, a.e.ListEntries())
	}
	if n := a.logs.count("已应用远端条目"); n != 0 {
		t.Fatalf("A 不应收到任何远端条目，实际 %d 次\n日志:\n%s", n, a.logs.dump())
	}
	after, _ := findEntry(a.e.ListEntries(), content)
	if !after.RecvAt.Equal(aEntry.RecvAt) {
		t.Fatalf("A 的条目 RecvAt 被刷新，说明收到了回播: %v → %v", aEntry.RecvAt, after.RecvAt)
	}
	if b.clip.get() != content {
		t.Fatalf("B 的剪贴板被改动: %q", b.clip.get())
	}
}

// S3：取回历史条目只写本机剪贴板，不广播。
func TestIntegrationPullDoesNotBroadcast(t *testing.T) {
	a, b := newPair(t)

	const first = "第一条"
	a.clip.set(first)
	waitClip(t, b, first)

	bBefore := b.logs.count("广播条目")
	aBefore := len(a.e.ListEntries())

	// B 取回自己历史里的那条（来自 A），也就是把它重新放进剪贴板。
	entry := b.e.ListEntries()[0]
	if err := b.e.PullEntry(entry.ID); err != nil {
		t.Fatalf("PullEntry 失败: %v", err)
	}
	waitClip(t, b, first)

	waitStable(400 * time.Millisecond)
	if n := b.logs.count("广播条目"); n != bBefore {
		t.Fatalf("取回不应触发广播，广播次数 %d → %d\n日志:\n%s", bBefore, n, b.logs.dump())
	}
	if n := len(a.e.ListEntries()); n != aBefore {
		t.Fatalf("取回不应影响 A 的历史，%d → %d", aBefore, n)
	}
}

// S6：重复内容不会重复入历史，也不会重复写剪贴板。
func TestIntegrationDuplicateContent(t *testing.T) {
	a, b := newPair(t)

	const dup = "重复的内容"
	a.clip.set(dup)
	waitClip(t, b, dup)

	// 换成别的内容并同步过去，让 B 的剪贴板离开 dup。
	a.clip.set("中间内容")
	waitClip(t, b, "中间内容")

	if n := len(b.e.ListEntries()); n != 2 {
		t.Fatalf("B 应有 2 条历史，得到 %d 条: %+v", n, b.e.ListEntries())
	}

	// 再复制一次相同内容：A 侧去重（历史仍是 2 条），广播照发；B 侧服务端去重→不 apply。
	a.clip.set(dup)
	waitStable(500 * time.Millisecond)

	if n := len(a.e.ListEntries()); n != 2 {
		t.Fatalf("A 的历史不应重复增长，得到 %d 条: %+v", n, a.e.ListEntries())
	}
	if got := b.clip.get(); got != "中间内容" {
		t.Fatalf("B 不应重复应用已存在的重复内容，剪贴板: %q", got)
	}
	if n := len(b.e.ListEntries()); n != 2 {
		t.Fatalf("B 的历史不应重复增长，得到 %d 条: %+v", n, b.e.ListEntries())
	}
}

// S5：暂停后不应用远端内容；恢复后继续工作。
func TestIntegrationPause(t *testing.T) {
	a, b := newPair(t)

	const before = "暂停前"
	a.clip.set(before)
	waitClip(t, b, before)

	b.e.SetPaused(true)
	if !b.e.Snapshot().Paused {
		t.Fatal("Snapshot.Paused 应为 true")
	}

	// A 侧不知道自己被暂停，照常广播；B 侧不应用。
	a.clip.set("暂停期间")
	waitFor(t, "B 历史记录暂停期间的条目", func() bool {
		_, ok := findEntry(b.e.ListEntries(), "暂停期间")
		return ok
	})
	waitStable(400 * time.Millisecond)
	if got := b.clip.get(); got != before {
		t.Fatalf("暂停时不应应用远端内容，剪贴板: %q", got)
	}
	if n := b.logs.count("已应用远端条目"); n != 1 {
		// 只有暂停前那一条被应用。
		t.Fatalf("暂停后不应再有 apply，实际 %d 次\n日志:\n%s", n, b.logs.dump())
	}

	// 恢复后，新内容应重新被应用。
	b.e.SetPaused(false)
	a.clip.set("恢复之后")
	waitClip(t, b, "恢复之后")
}

// S2 的开关：关闭 AutoApply 后远端内容只入历史，不写剪贴板。
func TestIntegrationAutoApplyOff(t *testing.T) {
	a, b := newPair(t)

	if err := b.e.SetAutoApply(false); err != nil {
		t.Fatalf("SetAutoApply(false) 失败: %v", err)
	}
	const content = "只入历史"
	a.clip.set(content)

	waitFor(t, "B 历史包含远端条目", func() bool {
		_, ok := findEntry(b.e.ListEntries(), content)
		return ok
	})
	waitStable(400 * time.Millisecond)
	if got := b.clip.get(); got != "" {
		t.Fatalf("AutoApply 关闭时不应写剪贴板，得到 %q", got)
	}

	if err := b.e.SetAutoApply(true); err != nil {
		t.Fatalf("SetAutoApply(true) 失败: %v", err)
	}
	a.clip.set("恢复自动应用")
	waitClip(t, b, "恢复自动应用")
}

// S4：强制广播当前剪贴板，即使轮询没有产生「变化」事件。
func TestIntegrationPushCurrent(t *testing.T) {
	a, b := newPair(t)

	const content = "推送当前内容"
	a.clip.set(content)
	waitClip(t, b, content)

	// 清掉 B 的历史，制造「B 侧没有这条」的状态。A 的剪贴板内容没变，
	// 轮询不会产生变化事件，因此只有显式的 PushCurrent 能把它重新送过去。
	if err := b.e.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory 失败: %v", err)
	}
	if n := len(b.e.ListEntries()); n != 0 {
		t.Fatalf("B 的历史应已清空，得到 %d 条", n)
	}

	before := a.logs.count("广播条目")
	if err := a.e.PushCurrent(); err != nil {
		t.Fatalf("PushCurrent 失败: %v", err)
	}
	waitFor(t, "B 重新收到推送的条目", func() bool {
		_, ok := findEntry(b.e.ListEntries(), content)
		return ok
	})
	if n := a.logs.count("广播条目"); n != before+1 {
		t.Fatalf("PushCurrent 应恰好广播一次，广播次数 %d → %d\n日志:\n%s", before, n, a.logs.dump())
	}
	if got := b.clip.get(); got != content {
		t.Fatalf("B 的剪贴板应为推送内容，得到 %q", got)
	}
}

// 暂停时显式推送被拒绝（S5 优先于 S4），并且不会静默丢失。
func TestIntegrationPushCurrentPaused(t *testing.T) {
	a, b := newPair(t)

	a.e.SetPaused(true)
	a.clip.set("暂停时推送")
	waitStable(400 * time.Millisecond)
	if err := a.e.PushCurrent(); !errors.Is(err, ErrPaused) {
		t.Fatalf("暂停时 PushCurrent 应返回 ErrPaused，得到 %v", err)
	}
	if got := b.clip.get(); got != "" {
		t.Fatalf("暂停时不应把内容送到 B，B 剪贴板: %q", got)
	}
}
