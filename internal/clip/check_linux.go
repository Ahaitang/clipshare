//go:build linux

package clip

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// CheckAvailable 检查 Linux 会话里的剪贴板命令是否就绪（spec §8 / §10）。
//
// 规则与 atotto/clipboard 的选择逻辑保持一致，但更严格：
//   - Wayland 会话（WAYLAND_DISPLAY 非空）必须同时有 wl-paste 与 wl-copy，
//     因为 X11 工具在 Wayland 下只能看到 XWayland 的剪贴板，会静默同步错内容；
//   - X11 会话需要 xclip 或 xsel；
//   - 两个会话变量都没有，说明是 headless/无图形会话，直接报错。
//
// 返回的错误里带可执行的安装命令，由 cmd/clipshare 打印后退出。
func CheckAvailable() error {
	wayland := strings.TrimSpace(os.Getenv("WAYLAND_DISPLAY"))
	display := strings.TrimSpace(os.Getenv("DISPLAY"))

	if wayland == "" && display == "" {
		return fmt.Errorf("未检测到图形会话（DISPLAY 与 WAYLAND_DISPLAY 均未设置），无法访问剪贴板；请在桌面会话的终端里运行 clipshare")
	}

	if wayland != "" {
		var missing []string
		for _, cmd := range []string{"wl-paste", "wl-copy"} {
			if _, err := exec.LookPath(cmd); err != nil {
				missing = append(missing, cmd)
			}
		}
		if len(missing) == 0 {
			return nil
		}
		hint := "请安装 wl-clipboard：sudo apt install wl-clipboard"
		if display != "" {
			hint += "；若你实际运行在 X11 会话下，请清掉 WAYLAND_DISPLAY 后重试"
		}
		return fmt.Errorf("Wayland 会话缺少剪贴板命令 %s：%s", strings.Join(missing, "、"), hint)
	}

	for _, cmd := range []string{"xclip", "xsel"} {
		if _, err := exec.LookPath(cmd); err == nil {
			return nil
		}
	}
	return fmt.Errorf("缺少剪贴板命令 xclip/xsel：请安装其一，例如 sudo apt install xclip（或 sudo apt install xsel）")
}
