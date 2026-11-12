//go:build linux

package picker

import (
	"fmt"
	"os/exec"
)

// show 在 Linux 上展示选择窗口。
//
// 优先使用自绘的 X11 窗口（见 x11_linux.go）：无额外依赖、与托盘同一套图形栈；
// 找不到可用字体或连不上 X 时，退回 zenity —— 它是 GNOME 自带的 GTK 列表对话框，
// 同样支持方向键选择与键入搜索，只是外观受主题控制、且是独立进程。
//
// 两级实现的意义：自绘是「体验目标」，zenity 是「永远有个能用的兜底」。
func show(items []Item, opts Options) (Result, error) {
	if res, err := showX11(items, opts); err == nil {
		return res, nil
	} else if err == errX11Unavailable {
		// 预期内的降级：字体或显示不可用，走 zenity。
		if path, lookErr := exec.LookPath("zenity"); lookErr == nil {
			return showZenity(path, items, opts)
		}
		return Result{}, fmt.Errorf("没有可用的选择窗口：X11 自绘不可用（%v），且未安装 zenity（sudo apt install zenity）", err)
	} else {
		return Result{}, err
	}
}
