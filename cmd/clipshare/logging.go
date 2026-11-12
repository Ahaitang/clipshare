package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

// logFileName 是配置目录下的日志文件名。
const logFileName = "clipshare.log"

// logMaxBytes 是日志文件上限；超过就在启动时轮转一次（保留 .1）。
const logMaxBytes = 2 << 20

// setupFileLog 让日志同时写入 <配置目录>/clipshare.log，返回关闭函数。
//
// 为什么需要：Windows 版编成了 GUI 子系统（Makefile 的 -H windowsgui），没有控制台
// 可以写 stderr，出问题时用户手里一无所有。写了文件之后，无论从托盘、计划任务
// 还是双击启动，都能事后查「为什么没同步上」。
//
// Linux 上 systemd 已经收 journal，这里再写一份文件也无害（便于把日志直接发给别人）。
// 失败不阻断启动：日志系统坏了不该让工具本身不能用。
func setupFileLog(cfgDir string) func() {
	if cfgDir == "" {
		return func() {}
	}
	path := filepath.Join(cfgDir, logFileName)
	rotateLogIfLarge(path)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("警告: 无法写入日志文件 %s（仅输出到标准错误）: %v", path, err)
		return func() {}
	}
	// 顺序很重要：io.MultiWriter 遇到第一个写失败就中止。
	// Windows GUI 子系统里 os.Stderr 是无效句柄、写入必失败，
	// 所以必须把文件放在前面，否则文件一直是 0 字节（这是实测踩到的坑）。
	log.SetOutput(io.MultiWriter(f, os.Stderr))
	log.Printf("---- 日志开始：pid=%d 文件=%s ----", os.Getpid(), path)
	return func() { _ = f.Close() }
}

// rotateLogIfLarge 在日志过大时把它改名成 .1（覆盖旧备份），避免无限增长。
func rotateLogIfLarge(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= logMaxBytes {
		return
	}
	if renameErr := os.Rename(path, path+".1"); renameErr != nil {
		// 轮转失败就继续追加，别为此中断启动。
		fmt.Fprintf(os.Stderr, "警告: 日志轮转失败: %v\n", renameErr)
	}
}
