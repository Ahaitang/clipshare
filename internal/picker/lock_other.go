//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package picker

// SingleInstance 在非 Unix 平台上是空操作。
//
// Windows 的快捷键走进程内 hotkey → picker.Run，而 show_windows.go 里已有
// 「同一时刻只允许一个选择窗口」的守卫（claimWindow），不需要额外的文件锁。
func SingleInstance(path string) (release func(), ok bool) {
	_ = path
	return func() {}, true
}
