//go:build windows

package picker

import "errors"

// show 在 Windows 上展示选择窗口（Win32 原生窗口，文本渲染交给系统，CJK 无额外依赖）。
//
// 实现约定：
//   - 无边框、置顶、居中；一个 Edit 搜索框 + 自绘/标准 ListBox 列表；
//   - ↑↓ 移动、PgUp/PgDn 翻页、Enter 确认、Esc 取消、鼠标点击选中、滚轮滚动；
//   - 键入即过滤（用 Filter）；
//   - 必须在创建窗口的同一个 goroutine/线程里跑消息循环（Win32 要求），
//     调用方应保证这一点（见 internal/hotkey 的热键线程）。
func show(items []Item, opts Options) (Result, error) {
	return Result{}, errors.New("Windows 选择窗口尚未实现")
}
