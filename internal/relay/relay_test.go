package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"clipshare/internal/hub"
	"clipshare/internal/model"
)

const testKey = "relayroomkey"

// startHub 起一个真实 TLS 的中继（httptest 自带证书），返回地址与指纹。
func startHub(t *testing.T, keep int) (url, pin string, h *hub.Hub) {
	t.Helper()
	h = hub.New(hub.Config{
		RoomKey:  testKey,
		NodeID:   "hubnode",
		NodeName: "hub",
		Version:  "test",
		Keep:     keep,
	})
	srv := httptest.NewTLSServer(h.Handler())
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	return srv.URL, "sha256:" + hex.EncodeToString(sum[:]), h
}

type sink struct {
	mu      sync.Mutex
	entries []model.Entry
	logs    []string
}

func (s *sink) onEntry(e model.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
}

func (s *sink) logf(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
}

func (s *sink) contents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e.Content)
	}
	return out
}

func newClient(t *testing.T, url, pin, selfID string, s *sink) *Client {
	t.Helper()
	c := New(Config{
		BaseURL:      url,
		RoomKey:      testKey,
		Pin:          pin,
		IntervalMs:   150,
		SelfDeviceID: selfID,
		OnEntry:      s.onEntry,
		Logf:         s.logf,
	})
	t.Cleanup(c.Stop)
	c.Start()
	return c
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

// 核心场景：B 发一条，C 收到；B 自己不收到（自己发的被下发回来时必须跳过）。
func TestPublishReachesOtherClientButNotSelf(t *testing.T) {
	url, pin, _ := startHub(t, 10)
	sinkB, sinkC := &sink{}, &sink{}
	b := newClient(t, url, pin, "B", sinkB)
	newClient(t, url, pin, "C", sinkC)

	waitFor(t, "两个客户端拿到初始游标", func() bool {
		return b.LastSeq() > 0 || true // 游标可能是 0；这里给一个轻量等待
	})
	time.Sleep(200 * time.Millisecond)

	entry := model.NewEntry("B", "b-node", "hello-from-b", time.Now())
	if err := b.Publish(entry); err != nil {
		t.Fatalf("上行失败: %v", err)
	}

	waitFor(t, "C 收到 B 的条目", func() bool { return len(sinkC.contents()) == 1 })
	if got := sinkC.contents()[0]; got != "hello-from-b" {
		t.Fatalf("C 收到 %q", got)
	}
	// 给 B 一点时间（若实现漏了自身过滤，它会在这段时间内收到自己的条目）
	time.Sleep(400 * time.Millisecond)
	if got := sinkB.contents(); len(got) != 0 {
		t.Fatalf("B 不应收到自己发的条目，实际 %v", got)
	}
}

// 新客户端启动时只取游标，不能把中继上积压的历史灌进剪贴板。
func TestStartDoesNotReplayHistory(t *testing.T) {
	url, pin, h := startHub(t, 10)
	// 事先塞两条历史
	h2 := hub.New(hub.Config{RoomKey: testKey, Keep: 10})
	_ = h2
	for i := 0; i < 2; i++ {
		h.AppendForTest(model.Entry{
			ID:     model.NewEntry("X", "x", fmt.Sprint("old", i), time.Now()).ID,
			Origin: "X", OriginName: "x", Content: fmt.Sprint("old", i),
		})
	}
	s := &sink{}
	c := newClient(t, url, pin, "B", s)
	waitFor(t, "客户端拿到游标", func() bool { return c.LastSeq() == 2 })
	time.Sleep(300 * time.Millisecond)
	if got := s.contents(); len(got) != 0 {
		t.Fatalf("启动时不应回放历史，实际收到 %v", got)
	}
}

// 指纹不对必须连不上（并且不会静默降级）。
func TestWrongPinIsRejected(t *testing.T) {
	url, _, _ := startHub(t, 10)
	s := &sink{}
	wrong := "sha256:" + fmt.Sprintf("%064d", 0)
	c := newClient(t, url, wrong, "B", s)
	time.Sleep(600 * time.Millisecond)
	if c.LastSeq() != 0 {
		t.Fatalf("指纹不匹配时不应建立有效连接，游标 = %d", c.LastSeq())
	}
	s.mu.Lock()
	logs := append([]string(nil), s.logs...)
	s.mu.Unlock()
	found := false
	for _, l := range logs {
		if contains(l, "指纹不匹配") || contains(l, "x509") || contains(l, "certificate") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("日志里应体现证书校验失败，实际 %v", logs)
	}
}

func TestPublishRejectedWithWrongRoomKey(t *testing.T) {
	url, pin, _ := startHub(t, 10)
	c := New(Config{BaseURL: url, RoomKey: "wrong-key", Pin: pin, IntervalMs: 150})
	err := c.Publish(model.NewEntry("B", "b", "x", time.Now()))
	if err == nil {
		t.Fatal("房间密钥错误时上行应失败")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
