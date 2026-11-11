//go:build !linux && !windows

package clip

// CheckAvailable 在非 Linux / Windows 平台不做检查：
// atotto/clipboard 在这些平台（如 macOS 的 pbcopy）自带实现。
func CheckAvailable() error { return nil }
