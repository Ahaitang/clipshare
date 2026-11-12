//go:build windows

package hotkey

import "errors"

// ErrNoDesktopIntegration 在 Windows 上不会出现：热键由进程内 RegisterHotKey
// 直接注册（见 hotkey_windows.go），不需要桌面环境配合。
var ErrNoDesktopIntegration = errors.New("当前平台不需要桌面快捷键注册")

// EnsureDesktopShortcut 在 Windows 上是空操作，返回空字符串与 nil。
//
// 保留同一个函数签名，是为了让 cmd 层不必写平台分支：
// Windows 靠 Start 里的 RegisterHotKey，Linux 靠这里登记桌面快捷键。
func EnsureDesktopShortcut(exePath string) (string, error) {
	_ = exePath
	return "", nil
}
