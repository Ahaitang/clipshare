package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"clipshare/internal/config"
	"clipshare/internal/picker"
)

// uiAddrFile 记录本地历史页**实际**监听的地址。
//
// 历史页优先用 port+1，但该端口被占用时会退回系统分配的随机端口（见 startWebUI）。
// `clipshare pick` 是独立进程，必须知道真实地址，所以由主进程把结果写到配置目录；
// 读不到时回退到约定端口 port+1。
const uiAddrFile = "ui.addr"

// runPick 实现 `clipshare pick`：取本机历史 → 弹选择窗口 → 选中项写回剪贴板。
//
// 它是「快捷键 → 选择 → 取回」这条链路的进程外入口：
// Linux 由桌面自定义快捷键拉起，Windows 由常驻进程内的全局热键直接调 picker.Run。
func runPick(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clipshare pick", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configDir := fs.String("config-dir", "", "配置目录（默认系统用户配置目录下的 clipshare/）")
	limit := fs.Int("limit", picker.DefaultLimit, "最多显示多少条历史")
	title := fs.String("title", "", "窗口标题")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "用法：clipshare pick [选项]\n\n从本机历史里选择一条，写回剪贴板（不会广播给其他机器）。\n\n选项：\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	dir := strings.TrimSpace(*configDir)
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			fmt.Fprintf(stderr, "clipshare: 定位配置目录失败: %v\n", err)
			return 1
		}
		dir = d
	}

	// 配置损坏时 Load 会返回重建后的默认配置 + 非 nil 错误；这里只警告不中断，
	// 因为 `pick` 只需要 deviceId 与端口，用默认值也能工作。
	cfg, err := config.Load(dir)
	if cfg == nil {
		fmt.Fprintf(stderr, "clipshare: 读取配置失败: %v\n", err)
		return 1
	}

	// 同一时刻只允许一个选择窗口：连按快捷键不该叠出好几个（见 picker.SingleInstance）。
	release, ok := picker.SingleInstance(filepath.Join(dir, "picker.lock"))
	if !ok {
		return 0 // 已有窗口在显示，静默退出即可，不打扰用户
	}
	defer release()

	base := resolveUIBase(dir, cfg.Port)
	client := picker.NewHTTPClient(base)

	entries, err := client.Entries()
	if err != nil {
		fmt.Fprintf(stderr, "clipshare: %v\n", err)
		fmt.Fprintf(stderr, "提示：请确认 clipshare 正在运行（托盘图标存在），或先执行 ./clipshare 启动。\n")
		return 1
	}
	if runErr := picker.Run(entries, client.Pull, cfg.DeviceID, picker.Options{
		Title:   *title,
		Limit:   *limit,
		PosFile: pickerPosFile(dir),
	}); runErr != nil {
		fmt.Fprintf(stderr, "clipshare pick: %v\n", runErr)
		return 1
	}
	return 0
}

// pickerPosFile 是记住选择窗口位置的文件路径（与 config.json 同目录）。
func pickerPosFile(cfgDir string) string {
	return filepath.Join(cfgDir, "picker.pos")
}

// resolveUIBase 决定历史页基址：优先读主进程写下的真实地址，否则用 port+1。
func resolveUIBase(dir string, syncPort int) string {
	if raw, err := os.ReadFile(filepath.Join(dir, uiAddrFile)); err == nil {
		if addr := strings.TrimSpace(string(raw)); addr != "" {
			return "http://" + addr
		}
	}
	uiPort := syncPort + uiAddrOffset
	if uiPort > 65535 {
		uiPort = 65535
	}
	return "http://127.0.0.1:" + strconv.Itoa(uiPort)
}
