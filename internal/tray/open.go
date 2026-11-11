package tray

import (
	"os/exec"
	"runtime"
	"strings"
)

// openTarget 用系统默认程序打开一个 http(s) 链接或本地目录。
//
// 这是 Options.OpenURL 的默认实现；打开失败只影响这一个动作，
// 由调用方记日志，不影响同步。
func openTarget(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		if isURL(target) {
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
		} else {
			cmd = exec.Command("explorer", target)
		}
	case "darwin":
		cmd = exec.Command("open", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不 Wait 会留下僵尸进程，而托盘进程活得很久，必须回收。
	go func() { _ = cmd.Wait() }()
	return nil
}

func isURL(target string) bool {
	return strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://")
}
