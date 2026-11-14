package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"clipshare/internal/model"
)

// feedTestResp 对应 /v1/feed 的响应体（实现里用 map 组装，这里按契约声明结构体）。
type feedTestResp struct {
	Items   []feedItem `json:"items"`
	LastSeq uint64     `json:"lastSeq"`
	Peers   int        `json:"peers"`
	Reset   bool       `json:"reset"`
}

const (
	testKey = "roomkey12345"
)

func newTestHub(t *testing.T, cfg Config) *Hub {
	t.Helper()
	if cfg.RoomKey == "" {
		cfg.RoomKey = testKey
	}
	if cfg.NodeID == "" {
		cfg.NodeID, cfg.NodeName = "hubnode", "hub"
	}
	if cfg.Keep == 0 {
		cfg.Keep = 10
	}
	return New(cfg)
}

func authReq(method, path, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	r.Header.Set("Authorization", "Bearer "+testKey)
	r.Header.Set("X-Room", model.RoomHash(testKey))
	return r
}

func TestPostItemAssignsIncreasingSeq(t *testing.T) {
	h := newTestHub(t, Config{})
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	for i := 1; i <= 3; i++ {
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, authReq("POST", "/v1/items", `{"content":"c`+fmt.Sprint(i)+`","origin":"B"}`))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("第 %d 条状态码 = %d", i, rec.Code)
		}
		var out struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out.Seq != uint64(i) {
			t.Fatalf("序号 = %d, want %d", out.Seq, i)
		}
	}
}

func TestAuthMatrix(t *testing.T) {
	h := newTestHub(t, Config{})
	cases := []struct {
		name string
		mod  func(*http.Request)
		want int
	}{
		{"无鉴权头", func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		{"密钥错误", func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }, http.StatusUnauthorized},
		{"X-Room 错误", func(r *http.Request) { r.Header.Set("X-Room", "deadbeef0000") }, http.StatusUnauthorized},
		{"正确", func(r *http.Request) {}, http.StatusAccepted},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		r := authReq("POST", "/v1/items", `{"content":"x","origin":"B"}`)
		c.mod(r)
		h.Handler().ServeHTTP(rec, r)
		if rec.Code != c.want {
			t.Errorf("%s: 状态码 = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestEmptyRoomKeyFailsClosed(t *testing.T) {
	// 没有配置密钥时必须拒绝一切（空==空 会被定长比较判为相等，是经典的 fail-open）。
	h := New(Config{Keep: 5})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/items", strings.NewReader(`{"content":"x"}`))
	r.Header.Set("Authorization", "Bearer ")
	h.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("空密钥应拒绝，状态码 = %d", rec.Code)
	}
}

func TestFeedCursorOnlyWhenSinceOmitted(t *testing.T) {
	h := newTestHub(t, Config{})
	for i := 0; i < 3; i++ {
		h.append(model.Entry{Content: fmt.Sprint("c", i), Origin: "B"})
	}
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, authReq("GET", "/v1/feed", ""))
	var resp feedTestResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 0 {
		t.Errorf("省略 since 不应回放历史，实际 %d 条", len(resp.Items))
	}
	if resp.LastSeq != 3 {
		t.Errorf("lastSeq = %d, want 3", resp.LastSeq)
	}
}

func TestFeedReturnsNewItems(t *testing.T) {
	h := newTestHub(t, Config{})
	h.append(model.Entry{Content: "a", Origin: "B"})
	h.append(model.Entry{Content: "b", Origin: "C"})

	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, authReq("GET", "/v1/feed?since=1", ""))
	var resp feedTestResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 1 || resp.Items[0].Entry.Content != "b" || resp.Items[0].Seq != 2 {
		t.Fatalf("增量结果不符: %+v", resp.Items)
	}
}

func TestFeedCursorSlidOutOfWindowResets(t *testing.T) {
	h := newTestHub(t, Config{Keep: 2})
	for i := 0; i < 5; i++ {
		h.append(model.Entry{Content: fmt.Sprint("c", i)})
	}
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, authReq("GET", "/v1/feed?since=1", ""))
	var resp feedTestResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Reset {
		t.Error("游标滑出窗口时应返回 reset，让客户端跳到最新")
	}
	if len(resp.Items) != 0 {
		t.Errorf("reset 时不应带条目，实际 %d 条", len(resp.Items))
	}
	if resp.LastSeq != 5 {
		t.Errorf("lastSeq = %d, want 5", resp.LastSeq)
	}
}

func TestFeedCursorAheadOfServer(t *testing.T) {
	// 服务端重启后序号归零：客户端的 since 会大于服务端，此时必须 reset 而不是报错。
	h := newTestHub(t, Config{})
	h.append(model.Entry{Content: "only"})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, authReq("GET", "/v1/feed?since=99", ""))
	var resp feedTestResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Reset || resp.LastSeq != 1 {
		t.Fatalf("游标超前时应 reset 到最新: %+v", resp)
	}
}

func TestFeedLongPollWakesOnAppend(t *testing.T) {
	h := newTestHub(t, Config{})
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	go func() {
		time.Sleep(120 * time.Millisecond)
		h.append(model.Entry{Content: "late", Origin: "C"})
	}()

	start := time.Now()
	req := authReq("GET", "/v1/feed?since=0&wait=3000", "")
	// since=0 但服务端 lastSeq=0 → 先回游标；这里改用 since=0 且已 append 前
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, req)
	var resp feedTestResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if time.Since(start) < 50*time.Millisecond {
		t.Log("长轮询提前返回（服务端序号为 0 时属正常路径）")
	}
	if len(resp.Items) != 1 || resp.Items[0].Entry.Content != "late" {
		t.Fatalf("长轮询未取到新条目: %+v", resp)
	}
}

func TestFeedLongPollTimeoutReturnsEmpty(t *testing.T) {
	h := newTestHub(t, Config{})
	h.append(model.Entry{Content: "seed"})

	start := time.Now()
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, authReq("GET", "/v1/feed?since=1&wait=200", ""))
	elapsed := time.Since(start)
	var resp feedTestResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) != 0 {
		t.Errorf("超时应回空列表，实际 %d 条", len(resp.Items))
	}
	if elapsed < 150*time.Millisecond {
		t.Errorf("应挂起到超时（实际 %v）", elapsed)
	}
}

func TestKeepTrimsRing(t *testing.T) {
	h := newTestHub(t, Config{Keep: 3})
	for i := 0; i < 10; i++ {
		h.append(model.Entry{Content: fmt.Sprint("c", i)})
	}
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, authReq("GET", "/v1/feed?since=0", ""))
	var resp feedTestResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Items) > 3 {
		t.Fatalf("窗口应只保留 3 条，实际 %d", len(resp.Items))
	}
}

func TestRateLimit(t *testing.T) {
	h := newTestHub(t, Config{RatePerMin: 2})
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	codes := []int{}
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.Handler().ServeHTTP(rec, authReq("POST", "/v1/items", `{"content":"x"}`))
		codes = append(codes, rec.Code)
	}
	if codes[2] != http.StatusTooManyRequests {
		t.Fatalf("第 3 次应被限流，实际 %v", codes)
	}
}

func TestHealthShape(t *testing.T) {
	h := newTestHub(t, Config{})
	h.append(model.Entry{Content: "x"})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/v1/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("health 状态码 = %d", rec.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["role"] != "hub" {
		t.Errorf("role = %v", got["role"])
	}
	if got["roomHash"] != model.RoomHash(testKey) {
		t.Errorf("roomHash = %v", got["roomHash"])
	}
}

func TestOnEntryCalled(t *testing.T) {
	got := make(chan string, 1)
	h := newTestHub(t, Config{OnEntry: func(e model.Entry) { got <- e.Content }})
	rec := httptest.NewRecorder()
	h.Handler().ServeHTTP(rec, authReq("POST", "/v1/items", `{"content":"applied","origin":"B"}`))
	select {
	case c := <-got:
		if c != "applied" {
			t.Errorf("OnEntry 收到 %q", c)
		}
	case <-time.After(time.Second):
		t.Fatal("OnEntry 未被调用")
	}
}
