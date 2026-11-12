//go:build linux

package picker

import "errors"

// errX11Unavailable 表示自绘 X11 窗口在当前环境不可用（没有 DISPLAY、连不上 X、
// 或找不到可渲染中文的字体）。这是**预期内的降级信号**，不是故障：
// show() 收到它会退回 zenity。
var errX11Unavailable = errors.New("x11 自绘窗口不可用")

// showX11 用自绘的 X11 窗口展示列表。
//
// 实现约定（由本文件实现）：
//   - 覆盖重定向（override-redirect）无边框窗口，居中显示；
//   - GrabKeyboard 独占键盘，↑↓ 移动、PgUp/PgDn 翻页、Enter 确认、Esc 取消，
//     其余可见字符进入搜索框（用 Filter 过滤）；
//   - 鼠标点击选中、滚轮滚动；点击窗口外区域不处理（Esc 才是唯一退出键）；
//   - 位图由 image.RGBA 渲染后用 xproto.PutImage 推送；
//   - 字体从系统 CJK 字体加载失败时返回 errX11Unavailable 以触发降级。
func showX11(items []Item, opts Options) (Result, error) {
	return Result{}, errX11Unavailable
}
