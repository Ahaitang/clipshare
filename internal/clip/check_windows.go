//go:build windows

package clip

// CheckAvailable 在 Windows 上恒为 nil：剪贴板走 Win32 API，没有外部命令依赖。
func CheckAvailable() error { return nil }
