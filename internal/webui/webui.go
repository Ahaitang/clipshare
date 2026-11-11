// Package webui 提供本机历史浏览页：一张内嵌单页 + 三个 JSON API。
//
// 页面只应绑定在 127.0.0.1 上：本包不做任何鉴权，能访问该端口的人
// 就可以读取历史并把条目写回剪贴板（design §9）。服务器地址由调用方决定。
package webui

import (
	_ "embed"
	"encoding/json"
	"log"
	"net/http"

	"clipshare/internal/engine"
	"clipshare/internal/model"
)

// indexHTML 是页面本体。原生 JS、无框架、无构建步骤（spec §9：≤ 250 行）。
//
//go:embed index.html
var indexHTML []byte

// Handler 返回挂在 127.0.0.1 上的历史页 handler。
//
// 路由：
//
//	GET  /ui             单页 HTML
//	GET  /ui/api/entries 历史条目，最新在前，[]model.Entry
//	POST /ui/api/pull    {"id":"..."} 写回本机剪贴板（S3，不广播）
//	POST /ui/api/clear   清空本地历史（不广播）
//
// entries 一次返回全部历史（默认上限 500 条），搜索与排序都在页面本地完成：
// 这个量级不需要服务端分页。
func Handler(ctrl engine.Controller) http.Handler {
	api := &api{ctrl: ctrl}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui", serveIndex)
	mux.HandleFunc("GET /ui/api/entries", api.listEntries)
	mux.HandleFunc("POST /ui/api/pull", api.pull)
	mux.HandleFunc("POST /ui/api/clear", api.clear)
	// 顺手让根路径跳到页面，免得用户从端口地址摸进来看到 404。
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui", http.StatusFound)
	})
	return mux
}

// serveIndex 返回内嵌页面。不缓存，升级二进制后立刻生效。
func serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(indexHTML); err != nil {
		log.Printf("webui: 写页面失败: %v", err)
	}
}

type api struct {
	ctrl engine.Controller
}

func (a *api) listEntries(w http.ResponseWriter, r *http.Request) {
	entries := a.ctrl.ListEntries()
	if entries == nil {
		// 契约要求返回数组：nil 切片会被编码成 null，页面就得额外判空。
		entries = []model.Entry{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, entries)
}

func (a *api) pull(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	// 按契约 PullEntry 只在找不到条目时返回错误，故映射为 404。
	if err := a.ctrl.PullEntry(req.ID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) clear(w http.ResponseWriter, r *http.Request) {
	if err := a.ctrl.ClearHistory(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("webui: 写响应失败: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
