// Package engine 组装剪贴板、历史、同步、发现等组件，并独占「同步语义」的实现。
//
// 本文件只定义 engine 暴露给界面层（internal/tray、internal/webui）的契约：
// 类型与接口，不含任何实现。同步行为规范见
// docs/superpowers/specs/2026-11-11-lan-clipboard-design.md §5（S1–S8）。
package engine

import "clipshare/internal/model"

// Snapshot 是给托盘/Web UI 的状态快照。
//
// 它是一次性拷贝：调用方可以随意读取，不会看到后续状态变化，
// 也没有任何字段能反向修改 engine。
type Snapshot struct {
	NodeName  string        // 本机展示名（config.NodeName）
	RoomHash  string        // 房间标识（model.RoomHash），密钥本身永不出网
	Paused    bool          // S5：同步已暂停
	AutoApply bool          // S2：收到远端条目是否自动写入本机剪贴板
	MDNS      bool          // mDNS 发现是否开启（design §7）
	Peers     int           // 当前在线同房间 peer 数
	LastError string        // 最近一次可展示的错误；空 = 无错误
	Recent    []model.Entry // 最新在前，长度由 engine 决定（通常为托盘可见条数）
}

// Controller 是 engine 暴露给界面的全部能力。tray 与 webui 只依赖它。
//
// 所有方法都必须并发安全：托盘菜单回调与 HTTP handler 会从不同 goroutine 调用。
type Controller interface {
	// Snapshot 返回当前状态快照，用于界面渲染（对应 §5 全部语义的可观测状态）。
	Snapshot() Snapshot

	// ListEntries 返回本地历史，最新在前；返回的是副本，调用方可随意持有。
	// 条目来源为 S1（本机复制）与 S2/S6（远端接收，重复内容已被 seen 集丢弃）。
	ListEntries() []model.Entry

	// PullEntry 把指定历史条目写回本机剪贴板，并让剪贴板轮询器把该内容记作已见
	// （clip.Poller.MarkSeen），从而不产生变化事件、绝不广播（S3）。
	// id 不存在时返回错误。
	PullEntry(id string) error

	// PushCurrent 强制广播当前剪贴板内容一次，绕过 MarkSeen 抑制（S4）。
	// 内容为空或超过 ItemMaxBytes 时不广播（S8）；广播失败只记日志不重试（S7）。
	PushCurrent() error

	// SetPaused 开关同步；暂停时不广播也不应用远端，但本地复制仍入历史（S5）。
	SetPaused(bool)

	// SetAutoApply 设置收到远端条目后是否自动写入本机剪贴板（S2）；
	// 配置落盘失败时返回错误。
	SetAutoApply(bool) error

	// SetMDNS 开关 mDNS 发现（design §7）；配置落盘失败时返回错误。
	SetMDNS(bool) error

	// ClearHistory 清空本地历史并落盘；不影响剪贴板内容，也不广播任何东西。
	ClearHistory() error
}
