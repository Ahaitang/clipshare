// Package webui 提供本机历史浏览页：一张内嵌单页 + 三个 JSON API。
//
// 页面只应绑定在 127.0.0.1 上：本包不做任何鉴权，能访问该端口的人
// 就可以读取历史并把条目写回剪贴板（design §9）。服务器地址由调用方决定。
package webui

import (
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"

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
// Options 是页面的可选能力。
type Options struct {
	// Restart 是「重启」按钮的回调（nil 时按钮置灰并说明不可用）。
	// 由 cmd 层注入：重启意味着退出并重新拉起进程，属于应用生命周期。
	Restart func()
	// Invite 返回本机邀请码（房间码 + 本机地址）；nil 时页面不显示邀请块。
	Invite func() (string, error)
	// Join 用一条邀请码加入别人的房间（写配置 + 重启）；nil 时页面不显示加入框。
	Join func(code string) error
	// Status 返回当前同步状态；nil 时页面不显示状态条。
	Status func() engine.Snapshot
	// Relay 返回中继客户端状态（URL 与是否连上）；nil 表示本机不做中继客户端。
	Relay func() (string, bool)
}

func Handler(ctrl engine.Controller, opts Options) http.Handler {
	api := &api{ctrl: ctrl, opts: opts}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ui", serveIndex)
	mux.HandleFunc("GET /ui/api/entries", api.listEntries)
	mux.HandleFunc("POST /ui/api/pull", api.pull)
	mux.HandleFunc("POST /ui/api/clear", api.clear)
	mux.HandleFunc("POST /ui/api/restart", api.restart)
	mux.HandleFunc("GET /ui/api/invite", api.invite)
	mux.HandleFunc("POST /ui/api/join", api.join)
	mux.HandleFunc("GET /ui/api/status", api.status)
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
	opts Options
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

// statusResponse 是 /ui/api/status 的响应体。
type statusResponse struct {
	NodeName  string `json:"nodeName"`
	RoomHash  string `json:"roomHash"`
	Peers     int    `json:"peers"`
	Paused    bool   `json:"paused"`
	AutoApply bool   `json:"autoApply"`
	MDNS      bool   `json:"mdns"`
	Entries   int    `json:"entries"`
	LastError string `json:"lastError"`
	RelayURL  string `json:"relayURL"`
	RelayOK   bool   `json:"relayConnected"`
}

// status 返回同步状态：不打开托盘也能知道「同步到几台了」，无头排障主要靠它。
func (a *api) status(w http.ResponseWriter, r *http.Request) {
	if a.opts.Status == nil {
		writeError(w, http.StatusNotImplemented, "当前模式下不提供状态")
		return
	}
	snap := a.opts.Status()
	resp := statusResponse{
		NodeName:  snap.NodeName,
		RoomHash:  snap.RoomHash,
		Peers:     snap.Peers,
		Paused:    snap.Paused,
		AutoApply: snap.AutoApply,
		MDNS:      snap.MDNS,
		Entries:   len(a.ctrl.ListEntries()),
		LastError: snap.LastError,
	}
	if a.opts.Relay != nil {
		resp.RelayURL, resp.RelayOK = a.opts.Relay()
	}
	writeJSON(w, http.StatusOK, resp)
}

// invite 返回本机邀请码。
func (a *api) invite(w http.ResponseWriter, r *http.Request) {
	if a.opts.Invite == nil {
		writeError(w, http.StatusNotImplemented, "当前模式下不提供邀请码")
		return
	}
	code, err := a.opts.Invite()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

// join 用邀请码加入别人的房间。成功后由 cmd 层负责重启使配置生效。
func (a *api) join(w http.ResponseWriter, r *http.Request) {
	if a.opts.Join == nil {
		writeError(w, http.StatusNotImplemented, "当前模式下不支持加入房间")
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是合法的 JSON")
		return
	}
	if err := a.opts.Join(strings.TrimSpace(body.Code)); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "joined"})
}

// restart 触发一次重启：先回 202，再在后台请求重启。
//
// 顺序很重要——如果先重启再回响应，客户端多半拿不到 202（进程已经没了），
// 页面会显示「重启失败」这种误导性错误。
func (a *api) restart(w http.ResponseWriter, r *http.Request) {
	if a.opts.Restart == nil {
		writeError(w, http.StatusNotImplemented, "当前以 --no-tray 或受限模式运行，无法重启")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"status":"restarting"}`))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go a.opts.Restart()
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
