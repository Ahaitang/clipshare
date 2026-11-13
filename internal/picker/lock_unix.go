//go:build linux || darwin || freebsd || netbsd || openbsd

package picker

import (
	"os"
	"path/filepath"
	"syscall"
)

// SingleInstance 尝试取得「同一时刻只允许一个选择窗口」的锁。
//
// 为什么需要：Linux 的快捷键是「桌面环境启动一个 clipshare pick 进程」，
// 连按几次就会起几个进程、叠出几个窗口，而 Esc 只能关掉抢到键盘的那一个
// （实测在桌面上堆了 9 个）。这里用 flock 做进程级互斥：拿不到锁就直接退出。
//
// 返回的 release 必须在使用方退出时调用（进程结束也会自动释放，但显式关掉更干净）。
// ok=false 表示已有实例在跑，调用方应当静默退出（不是错误）。
func SingleInstance(path string) (release func(), ok bool) {
	if path == "" {
		return func() {}, true
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}, true // 锁文件都建不了就别拦着用户用
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, true
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, false
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}
