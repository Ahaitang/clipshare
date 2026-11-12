package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clipshare/internal/config"
	"clipshare/internal/onboard"
	"clipshare/internal/picker"
)

// 这一组子命令解决「一台从没配置过的电脑怎么最快接进房间」：
//
//	clipshare invite        打印本机邀请码
//	clipshare join <邀请码>  加入别人的房间
//	clipshare init          交互式引导（第一台 / 加入已有）
//
// 之所以要有「邀请码」而不是让人抄 12 位房间码：不同网络下往往还得手填
// 对方地址（本项目所在网络禁多播）。把「房间码 + 对方地址」压成一条字符串，
// 复制粘贴一次就够，少一半出错机会。

// runInvite 打印本机邀请码。
func runInvite(args []string, stdout, stderr io.Writer) int {
	dir, cfg, code := commonFlags("clipshare invite", args, stderr)
	if code != -1 {
		return code
	}
	inv := onboard.Build(cfg)
	fmt.Fprintf(stdout, "邀请码：\n\n  %s\n\n", inv.String())
	fmt.Fprintf(stdout, "在另一台电脑上执行（装好 clipshare 后）：\n\n  clipshare join '%s'\n\n", inv.String())
	if len(inv.Seeds) == 0 {
		fmt.Fprintf(stdout, "提示：没能探测到本机局域网地址，邀请码里只有房间码；\n    对方需要能通过 mDNS 发现本机，或手动在配置里填 seeds。\n")
	}
	fmt.Fprintf(stdout, "本机配置目录：%s\n", dir)
	return 0
}

// runJoin 用邀请码加入别人的房间。
func runJoin(args []string, stdout, stderr io.Writer) int {
	var code string
	dir, cfg, rc := commonFlagsWithArg("clipshare join <邀请码>", args, stderr, &code)
	if rc != -1 {
		return rc
	}
	inv, err := onboard.Parse(code)
	if err != nil {
		fmt.Fprintf(stderr, "clipshare join: %v\n", err)
		return 2
	}
	onboard.Apply(cfg, inv)
	if err := cfg.Save(dir); err != nil {
		fmt.Fprintf(stderr, "clipshare join: 保存配置失败: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "已加入房间 %s\n", inv.RoomKey)
	if len(inv.Seeds) > 0 {
		fmt.Fprintf(stdout, "连接地址：%s\n", strings.Join(inv.Seeds, ", "))
	}

	// 正在运行的实例需要重启才会用上新配置；没在运行也没关系，下次启动生效。
	if restarted, rerr := requestDaemonRestart(dir, cfg); rerr != nil {
		fmt.Fprintf(stdout, "提示：未能让运行中的 clipshare 重启（%v），下次启动时会生效。\n", rerr)
	} else if restarted {
		fmt.Fprintf(stdout, "已让运行中的 clipshare 重启并应用新配置。\n")
	} else {
		fmt.Fprintf(stdout, "clipshare 当前未运行，下次启动时生效。\n")
	}
	fmt.Fprintf(stdout, "\n验证：打开历史页或问对方「与几台同步中」。\n")
	return 0
}

// runInit 交互式引导：第一台机器 / 加入已有房间。
func runInit(args []string, stdout, stderr io.Writer) int {
	dir, cfg, rc := commonFlags("clipshare init", args, stderr)
	if rc != -1 {
		return rc
	}

	fmt.Fprintf(stdout, "这台电脑还没有加入任何房间。请选择：\n\n")
	fmt.Fprintf(stdout, "  1) 我是第一台机器 —— 生成新房间，我会给你一条邀请码\n")
	fmt.Fprintf(stdout, "  2) 加入已有房间   —— 粘贴其他机器上的邀请码\n\n")
	fmt.Fprintf(stdout, "输入 1 或 2（回车确认）：")

	reader := bufio.NewReader(os.Stdin)
	choice, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(choice) == "" {
		fmt.Fprintf(stderr, "\nclipshare init: 读取输入失败（在管道/服务里请直接用 clipshare join <邀请码>）: %v\n", err)
		return 1
	}

	switch strings.TrimSpace(choice) {
	case "1":
		if err := cfg.Save(dir); err != nil {
			fmt.Fprintf(stderr, "保存配置失败: %v\n", err)
			return 1
		}
		inv := onboard.Build(cfg)
		fmt.Fprintf(stdout, "\n已生成新房间。邀请码：\n\n  %s\n\n", inv.String())
		fmt.Fprintf(stdout, "把这条发给其他人，让他们在新电脑上执行：\n\n  clipshare join '%s'\n\n", inv.String())
		fmt.Fprintf(stdout, "本机直接启动即可：clipshare\n")
		return 0
	case "2":
		fmt.Fprintf(stdout, "\n粘贴邀请码后回车：")
		line, err := reader.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			fmt.Fprintf(stderr, "\n读取邀请码失败: %v\n", err)
			return 1
		}
		inv, perr := onboard.Parse(strings.TrimSpace(line))
		if perr != nil {
			fmt.Fprintf(stderr, "邀请码无效: %v\n", perr)
			return 2
		}
		onboard.Apply(cfg, inv)
		if err := cfg.Save(dir); err != nil {
			fmt.Fprintf(stderr, "保存配置失败: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "\n已加入房间 %s，启动 clipshare 即可开始同步。\n", inv.RoomKey)
		return 0
	default:
		fmt.Fprintf(stderr, "未识别的选择 %q（只接受 1 或 2）\n", strings.TrimSpace(choice))
		return 2
	}
}

// commonFlags 解析这些子命令共用的 --config-dir，并加载配置。
func commonFlags(name string, args []string, stderr io.Writer) (string, *config.Config, int) {
	return commonFlagsWithArg(name, args, stderr, nil)
}

// commonFlagsWithArg 同上，但可选地取一个位置参数（join 的邀请码）。
func commonFlagsWithArg(name string, args []string, stderr io.Writer, first *string) (string, *config.Config, int) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	configDir := fs.String("config-dir", "", "配置目录（默认系统用户配置目录下的 clipshare/）")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "用法：%s [选项]\n\n选项：\n", name)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return "", nil, 0
		}
		return "", nil, 2
	}

	dir := strings.TrimSpace(*configDir)
	if dir == "" {
		d, err := config.Dir()
		if err != nil {
			fmt.Fprintf(stderr, "定位配置目录失败: %v\n", err)
			return "", nil, 1
		}
		dir = d
	}
	cfg, err := config.Load(dir)
	if cfg == nil {
		fmt.Fprintf(stderr, "读取配置失败: %v\n", err)
		return "", nil, 1
	}
	if first != nil {
		rest := fs.Args()
		if len(rest) < 1 || strings.TrimSpace(rest[0]) == "" {
			fmt.Fprintf(stderr, "%s: 缺少邀请码参数\n", name)
			return "", nil, 2
		}
		*first = rest[0]
	}
	return dir, cfg, -1
}

// requestDaemonRestart 请求正在运行的实例重启（走本机历史页 API）。
//
// 返回 restarted 表示「确实找到在跑的实例并让它重启了」；
// 连不上时返回 (false, err)，由调用方决定怎么提示。
func requestDaemonRestart(dir string, cfg *config.Config) (bool, error) {
	// 必须要求 ui.addr 存在：那是「本配置目录确实有实例在跑」的唯一凭据。
	// 否则回退到 port+1 会打到**另一个**配置目录的实例上，把无关进程重启掉
	// （实测：用临时配置目录 join 时误重启了主实例）。
	addrPath := filepath.Join(dir, uiAddrFile)
	if _, err := os.Stat(addrPath); err != nil {
		return false, nil
	}
	client := picker.NewHTTPClient(resolveUIBase(dir, cfg.Port))
	if _, err := client.Entries(); err != nil {
		return false, err // 没有在跑的实例
	}
	if err := client.Restart(); err != nil {
		return false, err
	}
	return true, nil
}

// joinRoom 是网页入口用的「加入房间」：解析邀请码 → 写配置 → 请求重启。
//
// 与 `clipshare join` 子命令共用同一套 onboard 逻辑，区别只是这里跑在
// 已经启动的进程内，配置目录与端口都是现成的。
func joinRoom(dir, code string) error {
	inv, err := onboard.Parse(code)
	if err != nil {
		return err
	}
	cfg, loadErr := config.Load(dir)
	if cfg == nil {
		return loadErr
	}
	onboard.Apply(cfg, inv)
	if err := cfg.Save(dir); err != nil {
		return err
	}
	// 稍后再重启：先把 202 响应送出去，否则页面拿不到「加入成功」的反馈。
	go func() {
		time.Sleep(300 * time.Millisecond)
		requestRestart()
	}()
	return nil
}
