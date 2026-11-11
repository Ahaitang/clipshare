package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clipshare/internal/model"
)

// TestClientPostSendsProtocolFields：请求路径、方法、鉴权头、房间头、JSON body 都要对。
func TestClientPostSendsProtocolFields(t *testing.T) {
	var (
		gotPath, gotMethod, gotAuth, gotRoom, gotCT string
		gotEntry                                    model.Entry
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		gotAuth = r.Header.Get("Authorization")
		gotRoom = r.Header.Get("X-Room")
		gotCT = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotEntry); err != nil {
			t.Errorf("解码请求体失败: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	e := model.NewEntry("node-a", "NODE-A", "hello", time.Now())
	c := &Client{}
	peer := Peer{NodeID: "node-b", Name: "NODE-B", BaseURL: srv.URL}
	if err := c.Post(context.Background(), peer, testRoomKey, e); err != nil {
		t.Fatalf("Post 失败: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("方法 = %s", gotMethod)
	}
	if gotPath != "/v1/items" {
		t.Fatalf("路径 = %s", gotPath)
	}
	if want := "Bearer " + testRoomKey; gotAuth != want {
		t.Fatalf("Authorization = %q，期望 %q", gotAuth, want)
	}
	if want := model.RoomHash(testRoomKey); gotRoom != want {
		t.Fatalf("X-Room = %q，期望 %q", gotRoom, want)
	}
	if !strings.Contains(gotCT, "application/json") {
		t.Fatalf("Content-Type = %q", gotCT)
	}
	if gotEntry.Content != "hello" || gotEntry.ID != e.ID || gotEntry.Origin != "node-a" {
		t.Fatalf("对端收到条目不一致: %+v（期望 %+v）", gotEntry, e)
	}
}

// TestClientPostTrimsTrailingSlash：BaseURL 带尾斜杠时路径不能变成 //v1/items。
func TestClientPostTrimsTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := &Client{}
	peer := Peer{BaseURL: srv.URL + "/"}
	if err := c.Post(context.Background(), peer, testRoomKey, testEntry("x")); err != nil {
		t.Fatalf("Post 失败: %v", err)
	}
	if gotPath != "/v1/items" {
		t.Fatalf("路径 = %q", gotPath)
	}
}

// TestClientPostNon2xx：对端拒收时返回带状态码的 PostError。
func TestClientPostNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"roomKey 不匹配"}`))
	}))
	defer srv.Close()

	c := &Client{}
	err := c.Post(context.Background(), Peer{Name: "NODE-B", BaseURL: srv.URL}, "wrong-key", testEntry("x"))
	if err == nil {
		t.Fatal("期望错误，实际 nil")
	}
	var postErr *PostError
	if !errors.As(err, &postErr) {
		t.Fatalf("期望 *PostError，实际 %T", err)
	}
	if postErr.Status != http.StatusUnauthorized {
		t.Fatalf("Status = %d，期望 401", postErr.Status)
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "NODE-B") {
		t.Fatalf("错误信息缺少状态码或节点名: %v", err)
	}
}

// TestClientPostTimeout：超过 Timeout 立即失败（不重试）。
func TestClientPostTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := &Client{Timeout: 30 * time.Millisecond}
	start := time.Now()
	err := c.Post(context.Background(), Peer{BaseURL: srv.URL}, testRoomKey, testEntry("x"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望超时错误，实际 nil")
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("耗时 %v，Timeout=30ms 时不应等这么久", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("期望 context.DeadlineExceeded，实际 %v", err)
	}
	var postErr *PostError
	if !errors.As(err, &postErr) || postErr.Status != 0 {
		t.Fatalf("超时应有 PostError 且 Status=0，实际 %v", err)
	}
}

// TestClientPostContextCanceled：调用方取消上下文后立刻返回。
func TestClientPostContextCanceled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Client{}
	start := time.Now()
	err := c.Post(ctx, Peer{BaseURL: srv.URL}, testRoomKey, testEntry("x"))
	if err == nil {
		t.Fatal("期望错误，实际 nil")
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("耗时 %v，取消上下文后应立即返回", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望 context.Canceled，实际 %v", err)
	}
}

// TestClientPostRejectsBadInput：缺少 BaseURL / roomKey 时不发请求，直接报错。
func TestClientPostRejectsBadInput(t *testing.T) {
	c := &Client{}

	if err := c.Post(context.Background(), Peer{Name: "NODE-B"}, testRoomKey, testEntry("x")); err == nil {
		t.Fatal("BaseURL 为空时应报错")
	}
	if err := c.Post(context.Background(), Peer{Name: "NODE-B", BaseURL: "http://127.0.0.1:1"}, "", testEntry("x")); err == nil {
		t.Fatal("roomKey 为空时应报错")
	} else if !strings.Contains(err.Error(), "roomKey") {
		t.Fatalf("错误信息应提到 roomKey: %v", err)
	}
}

// TestClientPostInvalidURL：BaseURL 不是合法 URL 时报错而不是 panic。
func TestClientPostInvalidURL(t *testing.T) {
	c := &Client{}
	err := c.Post(context.Background(), Peer{BaseURL: "http://[::1"}, testRoomKey, testEntry("x"))
	if err == nil {
		t.Fatal("非法 BaseURL 应报错")
	}
}

// TestPeerLabel：日志用的称呼包含节点名、短 ID 与地址。
func TestPeerLabel(t *testing.T) {
	p := Peer{NodeID: "abcdef0123456789", Name: "M-PC", BaseURL: "http://192.168.1.7:47100"}
	label := p.Label()
	for _, want := range []string{"M-PC", "abcdef01", "http://192.168.1.7:47100"} {
		if !strings.Contains(label, want) {
			t.Fatalf("Label() = %q，缺少 %q", label, want)
		}
	}

	// 缺字段时也不能出现空的括号内容。
	if got := (Peer{BaseURL: "http://x"}).Label(); !strings.Contains(got, "http://x") {
		t.Fatalf("Label() = %q", got)
	}
}
