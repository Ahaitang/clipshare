package sync

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"clipshare/internal/history"
	"clipshare/internal/model"
)

const testRoomKey = "k3y-abc"

// sink 收集 apply 回调收到的条目。
type sink struct {
	mu      stdsync.Mutex
	entries []model.Entry
}

func (s *sink) apply(e model.Entry) {
	s.mu.Lock()
	s.entries = append(s.entries, e)
	s.mu.Unlock()
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *sink) last() (model.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) == 0 {
		return model.Entry{}, false
	}
	return s.entries[len(s.entries)-1], true
}

type rig struct {
	server  *Server
	store   *history.Store
	sink    *sink
	handler http.Handler
}

// newRig 组装一个带 httptest 可直接驱动的服务端。maxBytes<=0 用默认上限。
func newRig(t *testing.T, maxBytes int) *rig {
	t.Helper()
	store := history.New(50, "", false)
	s := &sink{}
	srv := NewServer(ServerConfig{
		RoomKey:  testRoomKey,
		NodeID:   "node-a",
		NodeName: "NODE-A",
		Version:  "0.1.0",
		MaxBytes: maxBytes,
	}, store, s.apply)
	return &rig{server: srv, store: store, sink: s, handler: srv.Handler()}
}

// do 发一个原始请求。auth 为空表示不带 Authorization 头；room 同理。
func (r *rig) do(t *testing.T, method, path, auth, room, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if room != "" {
		req.Header.Set("X-Room", room)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func (r *rig) bearer() string   { return "Bearer " + testRoomKey }
func (r *rig) roomHash() string { return model.RoomHash(testRoomKey) }

// postEntry 用正确的鉴权发一条条目。
func (r *rig) postEntry(t *testing.T, e model.Entry) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("序列化条目失败: %v", err)
	}
	return r.do(t, http.MethodPost, "/v1/items", r.bearer(), r.roomHash(), string(raw))
}

func testEntry(content string) model.Entry {
	return model.Entry{
		ID:         "sender-id",
		Origin:     "node-b",
		OriginName: "NODE-B",
		Content:    content,
		// 发送方时钟故意偏到一小时后：服务端必须无视它。
		RecvAt: time.Now().Add(time.Hour),
	}
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("状态码 = %d，期望 %d，响应体: %s", rec.Code, want, rec.Body.String())
	}
}

// TestItemsAccepted：202 + 条目落到 store 和 apply，且 recvAt/ID 被服务端重写。
func TestItemsAccepted(t *testing.T) {
	r := newRig(t, 0)

	rec := r.postEntry(t, testEntry("hello"))
	assertStatus(t, rec, http.StatusAccepted)

	if got := r.sink.count(); got != 1 {
		t.Fatalf("apply 调用次数 = %d，期望 1", got)
	}
	e, _ := r.sink.last()
	if e.Content != "hello" {
		t.Fatalf("内容 = %q", e.Content)
	}
	if e.Origin != "node-b" || e.OriginName != "NODE-B" {
		t.Fatalf("来源信息丢失: %+v", e)
	}

	now := time.Now()
	if e.RecvAt.After(now.Add(time.Second)) || now.Sub(e.RecvAt) > 5*time.Second {
		t.Fatalf("recvAt 未被改写为服务端本地时间: %v（现在 %v）", e.RecvAt, now)
	}
	wantID := model.NewEntry(e.Origin, e.OriginName, e.Content, e.RecvAt).ID
	if e.ID != wantID {
		t.Fatalf("ID = %q，期望按 origin+content 推导的 %q", e.ID, wantID)
	}
	if r.store.Count() != 1 {
		t.Fatalf("store 条数 = %d，期望 1", r.store.Count())
	}
}

// TestItemsNormalizesContent：\r\n 与尾部 NUL 在入库前归一化。
func TestItemsNormalizesContent(t *testing.T) {
	r := newRig(t, 0)

	rec := r.postEntry(t, testEntry("a\r\nb\x00\x00"))
	assertStatus(t, rec, http.StatusAccepted)

	e, _ := r.sink.last()
	if e.Content != "a\nb" {
		t.Fatalf("归一化后内容 = %q，期望 %q", e.Content, "a\nb")
	}
}

// TestItemsDedupe：同内容第二条 202 但不 apply、不新增历史（S6）。
func TestItemsDedupe(t *testing.T) {
	r := newRig(t, 0)

	assertStatus(t, r.postEntry(t, testEntry("dup-content")), http.StatusAccepted)
	assertStatus(t, r.postEntry(t, testEntry("dup-content")), http.StatusAccepted)

	if got := r.sink.count(); got != 1 {
		t.Fatalf("去重后 apply 调用次数 = %d，期望 1", got)
	}
	if got := r.store.Count(); got != 1 {
		t.Fatalf("去重后 store 条数 = %d，期望 1", got)
	}
}

// TestItemsUnauthorized：401 的四种形态，且即使 X-Room 正确也不能通过。
func TestItemsUnauthorized(t *testing.T) {
	cases := []struct {
		name string
		auth string
	}{
		{"缺少 Authorization", ""},
		{"roomKey 错误", "Bearer wrong-key"},
		{"scheme 不是 Bearer", "Basic " + testRoomKey},
		{"Bearer 后为空", "Bearer "},
		{"只有 token 没有 scheme", testRoomKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 0)
			raw, _ := json.Marshal(testEntry("x"))
			rec := r.do(t, http.MethodPost, "/v1/items", tc.auth, r.roomHash(), string(raw))
			assertStatus(t, rec, http.StatusUnauthorized)
			if r.sink.count() != 0 || r.store.Count() != 0 {
				t.Fatal("鉴权失败时不应写入历史或触发 apply")
			}
		})
	}
}

// TestItemsForbiddenRoom：roomKey 对但房间标识不对 → 403（防误配串房间）。
func TestItemsForbiddenRoom(t *testing.T) {
	cases := []struct {
		name string
		room string
	}{
		{"缺少 X-Room", ""},
		{"X-Room 不匹配", "ffffffffffff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 0)
			raw, _ := json.Marshal(testEntry("x"))
			rec := r.do(t, http.MethodPost, "/v1/items", r.bearer(), tc.room, string(raw))
			assertStatus(t, rec, http.StatusForbidden)
			if r.sink.count() != 0 {
				t.Fatal("403 时不应触发 apply")
			}
		})
	}
}

// TestItemsBadRequest：内容为空/超限、非法 JSON 都是 400。
func TestItemsBadRequest(t *testing.T) {
	t.Run("内容为空", func(t *testing.T) {
		r := newRig(t, 0)
		assertStatus(t, r.postEntry(t, testEntry("")), http.StatusBadRequest)
		if r.sink.count() != 0 || r.store.Count() != 0 {
			t.Fatal("400 时不应写入历史")
		}
	})

	t.Run("归一化后为空", func(t *testing.T) {
		r := newRig(t, 0)
		assertStatus(t, r.postEntry(t, testEntry("\x00\x00")), http.StatusBadRequest)
	})

	t.Run("内容超限", func(t *testing.T) {
		r := newRig(t, 16)
		assertStatus(t, r.postEntry(t, testEntry(strings.Repeat("a", 17))), http.StatusBadRequest)
	})

	t.Run("内容恰好等于上限", func(t *testing.T) {
		r := newRig(t, 16)
		assertStatus(t, r.postEntry(t, testEntry(strings.Repeat("a", 16))), http.StatusAccepted)
	})

	t.Run("非法 JSON", func(t *testing.T) {
		r := newRig(t, 0)
		rec := r.do(t, http.MethodPost, "/v1/items", r.bearer(), r.roomHash(), "{not json")
		assertStatus(t, rec, http.StatusBadRequest)
	})

	t.Run("字段类型错误", func(t *testing.T) {
		r := newRig(t, 0)
		rec := r.do(t, http.MethodPost, "/v1/items", r.bearer(), r.roomHash(), `{"content":123}`)
		assertStatus(t, rec, http.StatusBadRequest)
	})

	t.Run("请求体远超上限", func(t *testing.T) {
		r := newRig(t, 64)
		rec := r.postEntry(t, testEntry(strings.Repeat("a", 100000)))
		assertStatus(t, rec, http.StatusBadRequest)
	})
}

// TestHealth：免鉴权返回四个字段，roomHash 由 roomKey 推导。
func TestHealth(t *testing.T) {
	r := newRig(t, 0)

	rec := r.do(t, http.MethodGet, "/v1/health", "", "", "")
	assertStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q", ct)
	}

	var got healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析 health 响应失败: %v（响应体 %q）", err, rec.Body.String())
	}
	want := healthResponse{NodeID: "node-a", Name: "NODE-A", Version: "0.1.0", RoomHash: model.RoomHash(testRoomKey)}
	if got != want {
		t.Fatalf("health = %+v，期望 %+v", got, want)
	}
}

// TestRoutesByMethod：方法不匹配回 405，未知路径回 404。
func TestRoutesByMethod(t *testing.T) {
	r := newRig(t, 0)
	assertStatus(t, r.do(t, http.MethodGet, "/v1/items", "", "", ""), http.StatusMethodNotAllowed)
	assertStatus(t, r.do(t, http.MethodPost, "/v1/health", "", "", ""), http.StatusMethodNotAllowed)
	assertStatus(t, r.do(t, http.MethodGet, "/v1/nope", "", "", ""), http.StatusNotFound)
}

// TestNilStoreStillApplies：store 未注入时不 panic，apply 照常执行。
func TestNilStoreStillApplies(t *testing.T) {
	s := &sink{}
	srv := NewServer(ServerConfig{RoomKey: testRoomKey, NodeID: "node-a"}, nil, s.apply)
	h := srv.Handler()

	raw, _ := json.Marshal(testEntry("hello"))
	req := httptest.NewRequest(http.MethodPost, "/v1/items", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+testRoomKey)
	req.Header.Set("X-Room", model.RoomHash(testRoomKey))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assertStatus(t, rec, http.StatusAccepted)
	if s.count() != 1 {
		t.Fatalf("apply 调用次数 = %d，期望 1", s.count())
	}
}
