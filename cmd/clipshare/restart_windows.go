//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// selfCommand 返回可执行文件与除 argv[0] 之外的参数。
func selfCommand() (string, []string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	return exe, os.Args[1:], nil
}

// spawnDetached 拉起一个「延迟启动」的新实例，然后由调用方退出当前进程。
//
// 为什么不用 PowerShell/cmd 包装（试过，并不稳）：PowerShell 子进程在某些
// 会话形态下起不来，而失败发生在另一个进程里、我们的日志什么都看不到。
// 这里改为直接启自身并加 `-startup-delay`，让新进程自己等旧进程释放端口：
// 少一层 shell、少一处可能静默失败的地方。
func spawnDetached(exe string, args []string) error {
	childArgs := append(append([]string{}, args...), "-startup-delay=2s")
	cmd := exec.Command(exe, childArgs...)
	// CREATE_NO_WINDOW：GUI 子系统程序本来不建控制台，这里再兜一层，
	// 避免有人把控制台变体（build-windows-console）用于自启时闪窗。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不 Wait：子进程要活过父进程。给它一点时间完成创建再让父进程退出。
	_ = time.AfterFunc(200*time.Millisecond, func() {})
	return nil
}
