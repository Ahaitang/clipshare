// Package picker 实现「按快捷键弹出历史列表、选择一条取回」的交互。
//
// 平台差异只在最后一步「怎么把列表画出来」：Linux 用 X11 自绘窗口，
// Windows 用 Win32 原生窗口。过滤、预览截断、条目排序、取回语义等纯逻辑
// 都放在本文件，因此可以在任意平台上单测，不必依赖 GUI 环境。
//
// 取回走的是 S3 语义（写本机剪贴板但不广播），由 engine.PullEntry 保证。
package picker

import (
	"fmt"
	"strings"

	"clipshare/internal/model"
)

// DefaultLimit 是选择窗口默认展示的条目数上限。
const DefaultLimit = 50

// Item 是选择窗口里的一行。
type Item struct {
	ID      string
	Source  string // 「本机」或来源节点名
	Content string // 原始内容（可能多行）
	When    string // 预格式化的时间，按平台惯例由调用方给出
}

// Result 是选择结果。OK=false 表示用户取消（Esc / 关窗）。
type Result struct {
	ID string
	OK bool
}

// PullFunc 把选中的条目写回本机剪贴板（engine.PullEntry 或 HTTP 适配器实现）。
//
// 用函数而不是接口，是为了让「进程内」（Windows 全局热键，直接调 engine）与
// 「进程外」（Linux 快捷键起子进程，走本机 HTTP）共用同一套流程，
// 同时避免 picker 反向依赖 engine 造成循环导入。
type PullFunc func(id string) error

// Options 是展示参数。
type Options struct {
	Title string // 窗口标题
	Limit int    // 0 表示 DefaultLimit
}

// showFn 是平台实现的可替换句柄：生产环境指向 show（见 show_*.go），
// 单测里可以替换成假实现，从而在没有 GUI 的 CI 上覆盖 Run 的完整流程。
var showFn = show

// Run 是选择流程的入口：弹窗 → 选中后调用 pull 写回剪贴板（不广播）。
//
// selfDeviceID 用于把来源标成「本机」；来自其它节点的条目显示其节点名。
// 用户取消时返回 nil（不是错误）。
func Run(entries []model.Entry, pull PullFunc, selfDeviceID string, opts Options) error {
	if len(entries) == 0 {
		return fmt.Errorf("剪贴板历史为空，先复制一些内容再试")
	}
	items := FromEntries(entries, selfDeviceID)
	if opts.Limit <= 0 {
		opts.Limit = DefaultLimit
	}
	if len(items) > opts.Limit {
		items = items[:opts.Limit]
	}

	res, err := showFn(items, opts)
	if err != nil {
		return err
	}
	if !res.OK || res.ID == "" {
		return nil
	}
	if pull == nil {
		return fmt.Errorf("picker: pull 回调为空")
	}
	return pull(res.ID)
}

// FromEntries 把历史条目转成展示项。顺序沿用 clock-wise 的「最新在前」。
func FromEntries(entries []model.Entry, selfDeviceID string) []Item {
	items := make([]Item, 0, len(entries))
	for _, e := range entries {
		src := e.OriginName
		if e.Origin == selfDeviceID || e.Origin == "" {
			src = "本机"
		}
		items = append(items, Item{
			ID:      e.ID,
			Source:  src,
			Content: e.Content,
			When:    e.RecvAt.Local().Format("01-02 15:04"),
		})
	}
	return items
}

// Filter 按查询词过滤：大小写不敏感的子串匹配，同时匹配内容与来源。
//
// 空查询返回原列表。顺序不变（不做重排），避免用户在输入时看到列表乱跳。
func Filter(items []Item, query string) []Item {
	query = strings.TrimSpace(query)
	if query == "" {
		return items
	}
	q := strings.ToLower(query)
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if strings.Contains(strings.ToLower(it.Content), q) ||
			strings.Contains(strings.ToLower(it.Source), q) {
			out = append(out, it)
		}
	}
	return out
}

// Preview 生成单行预览：换行折叠成 ⏎，按 rune 截断，避免把窗口撑宽。
func Preview(content string, maxRunes int) string {
	oneLine := strings.ReplaceAll(content, "\r\n", "\n")
	oneLine = strings.Join(strings.Split(oneLine, "\n"), " ⏎ ")
	oneLine = strings.ReplaceAll(oneLine, "\t", "  ")
	return model.Truncate(oneLine, maxRunes)
}

// MoveIndex 把选中下标按 delta 移动并夹在 [0, n) 内；n<=0 返回 0。
// 供各平台实现复用，保证方向键行为一致。
func MoveIndex(cur, delta, n int) int {
	if n <= 0 {
		return 0
	}
	cur += delta
	if cur < 0 {
		return 0
	}
	if cur >= n {
		return n - 1
	}
	return cur
}
