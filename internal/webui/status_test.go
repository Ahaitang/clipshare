package webui

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clipshare/internal/engine"
	"clipshare/internal/model"
)

var errTest = errors.New("测试用错误")

func newRequest(method, path, body string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestStatusEndpoint(t *testing.T) {
	ctrl := &fakeController{
		snapshot: engine.Snapshot{NodeName: "haitang", RoomHash: "abc123", Peers: 2, MDNS: true, AutoApply: true},
		entries:  []model.Entry{{ID: "1"}, {ID: "2"}, {ID: "3"}},
	}
	h := Handler(ctrl, Options{Status: ctrl.Snapshot})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("GET", "/ui/api/status", ""))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var got statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.NodeName != "haitang" || got.RoomHash != "abc123" || got.Peers != 2 {
		t.Errorf("字段不符: %+v", got)
	}
	if got.Entries != 3 {
		t.Errorf("条目数 = %d, want 3", got.Entries)
	}
}

func TestStatusEndpointWithoutCallback(t *testing.T) {
	// 没注入 Status 时要明确 501，而不是返回一份全零的假状态。
	h := Handler(&fakeController{}, Options{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("GET", "/ui/api/status", ""))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("状态码 = %d, want 501", rec.Code)
	}
}

func TestInviteEndpoint(t *testing.T) {
	h := Handler(&fakeController{}, Options{
		Invite: func() (string, error) { return "clipshare1:room@1.2.3.4:47100", nil },
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("GET", "/ui/api/invite", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["code"] != "clipshare1:room@1.2.3.4:47100" {
		t.Errorf("邀请码 = %q", got["code"])
	}
}

func TestJoinEndpoint(t *testing.T) {
	var got string
	h := Handler(&fakeController{}, Options{
		Join: func(code string) error { got = code; return nil },
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newRequest("POST", "/ui/api/join", `{"code":"  clipshare1:room@1.2.3.4:47100  "}`))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("状态码 = %d, want 202", rec.Code)
	}
	if got != "clipshare1:room@1.2.3.4:47100" {
		t.Errorf("传给 Join 的邀请码 = %q（应去掉两侧空白）", got)
	}
}

func TestJoinEndpointRejects(t *testing.T) {
	cases := []struct {
		name string
		body string
		opt  Options
		want int
	}{
		{"未启用", `{"code":"x"}`, Options{}, http.StatusNotImplemented},
		{"坏 JSON", `{`, Options{Join: func(string) error { return nil }}, http.StatusBadRequest},
		{"加入失败", `{"code":"bad"}`, Options{Join: func(string) error { return errTest }}, http.StatusBadRequest},
	}
	for _, c := range cases {
		h := Handler(&fakeController{}, c.opt)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, newRequest("POST", "/ui/api/join", c.body))
		if rec.Code != c.want {
			t.Errorf("%s: 状态码 = %d, want %d", c.name, rec.Code, c.want)
		}
	}
}
