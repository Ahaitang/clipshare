package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"clipshare/internal/engine"
	"clipshare/internal/model"
)

// fakeController 是测试用的 engine.Controller：记录被调用的动作，不碰真实剪贴板。
type fakeController struct {
	mu       sync.Mutex
	snapshot engine.Snapshot
	entries  []model.Entry
	pulled   []string
	clears   int
	pullErr  error
	clearErr error
}

func (f *fakeController) Snapshot() engine.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeController) ListEntries() []model.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.entries == nil {
		return nil
	}
	out := make([]model.Entry, len(f.entries))
	copy(out, f.entries)
	return out
}

func (f *fakeController) PullEntry(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pullErr != nil {
		return f.pullErr
	}
	f.pulled = append(f.pulled, id)
	return nil
}

func (f *fakeController) PushCurrent() error { return nil }
func (f *fakeController) SetPaused(bool)     {}

func (f *fakeController) SetAutoApply(bool) error { return nil }
func (f *fakeController) SetMDNS(bool) error      { return nil }

func (f *fakeController) ClearHistory() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clearErr != nil {
		return f.clearErr
	}
	f.clears++
	f.entries = nil
	return nil
}

func (f *fakeController) pulledIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pulled...)
}

func do(t *testing.T, ctrl engine.Controller, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	Handler(ctrl).ServeHTTP(rec, req)
	return rec
}

func TestIndexPage(t *testing.T) {
	rec := do(t, &fakeController{}, http.MethodGet, "/ui", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{"<!doctype html>", "clipshare", "/ui/api/entries", "/ui/api/pull", "/ui/api/clear"} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面缺少 %q", want)
		}
	}
}

func TestRootRedirectsToUI(t *testing.T) {
	rec := do(t, &fakeController{}, http.MethodGet, "/", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/ui" {
		t.Fatalf("Location = %q, want /ui", loc)
	}
}

func TestEntriesAPI(t *testing.T) {
	at := time.Date(2026, 11, 11, 18, 30, 0, 0, time.UTC)
	ctrl := &fakeController{entries: []model.Entry{
		{ID: "b", Origin: "dev-a", OriginName: "A-PC", Content: "远端内容", RecvAt: at},
		{ID: "a", Content: "本机内容", RecvAt: at.Add(-time.Minute)},
	}}
	rec := do(t, ctrl, http.MethodGet, "/ui/api/entries", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}

	var got []model.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("返回顺序/条数不对: %+v", got)
	}
	if got[0].OriginName != "A-PC" || got[0].Content != "远端内容" || !got[0].RecvAt.Equal(at) {
		t.Fatalf("字段丢失: %+v", got[0])
	}
}

func TestEntriesAPIEmptyIsArray(t *testing.T) {
	rec := do(t, &fakeController{}, http.MethodGet, "/ui/api/entries", "")
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("空历史应返回 []，实际 %q", got)
	}
}

func TestPullAPI(t *testing.T) {
	ctrl := &fakeController{}
	rec := do(t, ctrl, http.MethodPost, "/ui/api/pull", `{"id":"entry-1"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204；body=%s", rec.Code, rec.Body.String())
	}
	if ids := ctrl.pulledIDs(); len(ids) != 1 || ids[0] != "entry-1" {
		t.Fatalf("PullEntry 调用参数不对: %v", ids)
	}
}

func TestPullAPIRejectsBadRequest(t *testing.T) {
	cases := []struct {
		name, body string
		want       int
	}{
		{"坏 JSON", `{"id":`, http.StatusBadRequest},
		{"缺 id", `{}`, http.StatusBadRequest},
		{"空 id", `{"id":""}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := &fakeController{}
			rec := do(t, ctrl, http.MethodPost, "/ui/api/pull", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if ids := ctrl.pulledIDs(); len(ids) != 0 {
				t.Fatalf("非法请求不应调用 PullEntry: %v", ids)
			}
			if !strings.Contains(rec.Body.String(), "error") {
				t.Fatalf("错误响应应带 error 字段: %s", rec.Body.String())
			}
		})
	}
}

func TestPullAPINotFound(t *testing.T) {
	ctrl := &fakeController{pullErr: errors.New("条目不存在")}
	rec := do(t, ctrl, http.MethodPost, "/ui/api/pull", `{"id":"nope"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "条目不存在") {
		t.Fatalf("应回传错误原因: %s", rec.Body.String())
	}
}

func TestClearAPI(t *testing.T) {
	ctrl := &fakeController{entries: []model.Entry{{ID: "a", Content: "x"}}}
	rec := do(t, ctrl, http.MethodPost, "/ui/api/clear", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if ctrl.clears != 1 {
		t.Fatalf("ClearHistory 调用次数 = %d, want 1", ctrl.clears)
	}
	if got := do(t, ctrl, http.MethodGet, "/ui/api/entries", "").Body.String(); strings.TrimSpace(got) != "[]" {
		t.Fatalf("清空后应无条目，实际 %q", got)
	}
}

func TestClearAPIError(t *testing.T) {
	ctrl := &fakeController{clearErr: errors.New("磁盘只读")}
	rec := do(t, ctrl, http.MethodPost, "/ui/api/clear", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "磁盘只读") {
		t.Fatalf("应回传错误原因: %s", rec.Body.String())
	}
}

func TestMethodNotAllowed(t *testing.T) {
	ctrl := &fakeController{}
	if rec := do(t, ctrl, http.MethodGet, "/ui/api/pull", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /ui/api/pull status = %d, want 405", rec.Code)
	}
	if rec := do(t, ctrl, http.MethodPost, "/ui/api/entries", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /ui/api/entries status = %d, want 405", rec.Code)
	}
}

// TestConcurrentAccess 让 -race 覆盖 handler 并发：托盘与页面会同时打 engine。
func TestConcurrentAccess(t *testing.T) {
	ctrl := &fakeController{entries: []model.Entry{{ID: "a", Content: "x"}}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			do(t, ctrl, http.MethodGet, "/ui/api/entries", "")
			do(t, ctrl, http.MethodPost, "/ui/api/pull", `{"id":"a"}`)
			do(t, ctrl, http.MethodGet, "/ui", "")
		}()
	}
	wg.Wait()
	if ids := ctrl.pulledIDs(); len(ids) != 8 {
		t.Fatalf("并发 PullEntry 次数 = %d, want 8", len(ids))
	}
}
