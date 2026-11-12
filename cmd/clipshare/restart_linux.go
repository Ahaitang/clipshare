//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// selfCommand 返回可执行文件与除 argv[0] 之外的参数。
func selfCommand() (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	return exe, os.Args[1:], nil
}

// spawnDetached 用 exec 直接替换当前进程映像。
//
// Linux 上这是最干净的重启方式：PID 不变（systemd 的 MainPID 不会抖），
// 也没有「旧进程还没释放端口、新进程抢不到」的竞态。
func spawnDetached(exe string, args []string) error {
	argv := append([]string{exe}, args...)
	if err := syscall.Exec(exe, argv, os.Environ()); err != nil {
		return err
	}
	return nil // 正常情况下永远不会走到这里（进程映像已被替换）
}
