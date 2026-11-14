// Command clipshare 是局域网剪贴板共享工具的入口。
//
// 本文件只做「组装」：解析 flag、加载配置、创建 history / clip / engine、
// 启动 HTTP 服务与本地历史页、拉起托盘或等待信号退出。
// 同步语义（S1–S8）全部在 internal/engine，这里不重复实现任何规则。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"clipshare/internal/clip"
	"clipshare/internal/config"
	"clipshare/internal/engine"
	"clipshare/internal/history"
	"clipshare/internal/model"
	"clipshare/internal/onboard"
	"clipshare/internal/tray"
	"clipshare/internal/webui"
)

// version 是构建时注入的版本号：
//
//	go build -ldflags "-X main.version=0.1.0"
//
// 没有注入时保持 "dev"，便于区分手工 `go build` 的产物。
// 启动时还会把同一个值写进 engine.Version，保证 `--version`、`/v1/health`
// 与 mDNS 上报的版本永远一致。
var version = "dev"

// uiAddrOffset 是本地历史页相对同步端口的偏移。
//
// 同步端口（0.0.0.0:port）由 engine.Start() 独占，main 不预先 listen、也不抢这个端口；
// 历史页只能在回环地址上另开一个端口，用 port+1 是为了可预测、方便收藏。
// 该端口被占用时退回系统分配的空闲端口，实际地址一律以启动日志为准。
const uiAddrOffset = 1

func main() {
	log.SetFlags(log.LstdFlags)
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 返回进程退出码，便于单测覆盖参数解析与 --version 路径。
func run(args []string, stdout, stderr io.Writer) int {
	// 子命令：pick（快捷键拉起的选择窗口）与 onboarding 三件套。
	if len(args) > 0 {
		switch args[0] {
		case "pick":
			return runPick(args[1:], stdout, stderr)
		case "invite":
			return runInvite(args[1:], stdout, stderr)
		case "join":
			return runJoin(args[1:], stdout, stderr)
		case "init":
			return runInit(args[1:], stdout, stderr)
		case "status":
			return runStatus(args[1:], stdout, stderr)
		case "hub-info":
			return runHubInfo(args[1:], stdout, stderr)
		}
	}

	opts, err := parseOptions(args, stdout, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintf(stderr, "clipshare: %v\n", err)
		return 2
	}
	if opts.showVersion {
		fmt.Fprintf(stdout, "clipshare %s\n", version)
		return 0
	}
	if opts.startupDelay > 0 {
		log.Printf("按 -startup-delay=%s 延迟启动（等待旧进程释放端口）", opts.startupDelay)
		time.Sleep(opts.startupDelay)
	}
	if err := serve(opts); err != nil {
		fmt.Fprintf(stderr, "clipshare: %v\n", err)
		return 1
	}
	// 重启请求：此时 serve 的 defer 已经全部跑完（engine 停、历史落盘、UI 关闭），
	// 端口也释放了，可以安全地拉起新实例。
	if restartRequested() {
		if err := relaunch(); err != nil {
			// GUI 子系统没有 stderr，必须同时写日志文件，否则用户完全看不到原因。
			log.Printf("重启失败: %v", err)
			fmt.Fprintf(stderr, "clipshare: 重启失败: %v\n", err)
			return 1
		}
		return 0
	}
	return 0
}

// options 是命令行参数。未显式给出的项保持零值，表示「不覆盖配置文件」。
type options struct {
	room        string
	port        int
	name        string
	seeds       multiFlag
	noTray      bool
	noPersist   bool
	mdns        bool // 默认 true：--mdns=false 才关闭
	mdnsSet     bool // 是否显式指定过 --mdns（决定要不要覆盖配置文件）
	configDir   string
	showVersion bool
	// startupDelay 让进程启动后先等一会儿再监听端口。
	// 只用于 Windows 的重启：那边的重启方式是「拉起新进程、父进程退出」，
	// 新进程必须等旧进程释放 47100，否则绑定失败直接退出。
	startupDelay time.Duration
}

// multiFlag 支持重复出现的 --seed。
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return errors.New("seed 不能为空，格式为 host:port")
	}
	*m = append(*m, v)
	return nil
}

func parseOptions(args []string, stdout, stderr io.Writer) (*options, error) {
	opts := &options{mdns: true}
	fs := flag.NewFlagSet("clipshare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "clipshare %s —— 局域网剪贴板共享\n\n用法：clipshare [选项]\n\n不写选项时完全使用配置文件（首次启动自动生成）。flag 优先级高于配置文件。\n\n选项：\n", version)
		fs.PrintDefaults()
	}

	fs.StringVar(&opts.room, "room", "", "房间码，覆盖配置里的 roomKey；多台机器必须填同一个")
	fs.IntVar(&opts.port, "port", 0, "HTTP 监听端口，覆盖配置里的 port（默认 47100）")
	fs.StringVar(&opts.name, "name", "", "本机显示名，覆盖配置里的 nodeName（默认主机名）")
	fs.Var(&opts.seeds, "seed", "追加一个静态节点地址 host:port（可重复指定；给禁多播网络用）")
	fs.BoolVar(&opts.noTray, "no-tray", false, "不初始化托盘，改用本地历史页（无 DBus / 无 AppIndicator 扩展时用）")
	fs.BoolVar(&opts.noPersist, "no-persist", false, "不把历史落盘（等价于配置里的 persist: false）")
	fs.BoolVar(&opts.mdns, "mdns", true, "是否启用 mDNS 自动发现；用 --mdns=false 关闭，改为只连 seeds")
	fs.StringVar(&opts.configDir, "config-dir", "", "配置目录（默认取系统用户配置目录下的 clipshare/）")
	fs.BoolVar(&opts.showVersion, "version", false, "打印版本号后退出")
	fs.DurationVar(&opts.startupDelay, "startup-delay", 0, "内部用：启动后延迟指定时间再监听端口（重启时避免与旧进程抢端口）")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("多余的参数: %s（用法：clipshare [选项]）", strings.Join(fs.Args(), " "))
	}
	// 只有用户显式写过 --mdns，才用它覆盖配置文件里的值。
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "mdns" {
			opts.mdnsSet = true
		}
	})
	return opts, nil
}

// serve 是真正的组装与运行：返回 nil 表示正常退出（信号或托盘退出）。
func serve(opts *options) error {
	dir, err := resolveConfigDir(opts)
	if err != nil {
		return err
	}
	// 配置目录不存在时创建（0700，因为里面有房间密钥）。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
	}

	// 日志落文件：Windows GUI 子系统没有控制台可写，出问题时只能靠这个文件。
	// 把它放在配置目录里，和 config.json/history.json 在一起，便于一起打包发给别人。
	// 故意不 defer 关闭：重启路径里 run() 会在 serve() 返回**之后**才重新拉起进程，
	// 那时如果文件已关闭，重启日志（包括失败原因）就全丢了——实测 Windows 上正是
	// 因为这一点看不到任何线索。守护进程的日志句柄活到进程退出即可，由操作系统回收。
	_ = setupFileLog(dir)

	firstRun := !fileExists(config.Path(dir))
	cfg, err := loadConfig(dir, opts)
	if err != nil {
		return err
	}

	// 历史：载入失败只告警，从空历史起步（spec §10：不阻塞启动）。
	store := history.New(cfg.HistoryLimit, config.HistoryPath(dir), cfg.Persist)
	if loadErr := store.Load(); loadErr != nil {
		log.Printf("警告: %v（从空历史起步，不影响同步）", loadErr)
	}

	// 剪贴板：engine 故意不做自检（方便测试注入假剪贴板），自检放在这里。
	// Linux 缺外部命令时把带安装命令的错误原样打到 stderr 后退出，绝不静默空转。
	if checkErr := clip.CheckAvailable(); checkErr != nil {
		return checkErr
	}
	board, err := clip.New()
	if err != nil {
		return err
	}

	// 版本链路：--version 打印 main.version，/v1/health 与 mDNS 上报 engine.Version，
	// 必须在 Start 之前用同一个值覆盖，否则两台机器看到的版本会和二进制对不上。
	engine.Version = version

	eng := engine.New(cfg, store, board, log.Printf)
	if startErr := eng.Start(); startErr != nil {
		return startErr
	}
	defer eng.Stop()

	// 中继服务端角色（中心点 A）：与剪贴板、局域网角色互相独立，可同时开启。
	if cfg.HubEnabled {
		hubStop, hubErr := startHub(cfg, dir, func(e model.Entry) { eng.IngestRemote(e) })
		if hubErr != nil {
			return hubErr
		}
		defer hubStop()
	}

	// 中继客户端角色（B/C）：把本机变化上行到中心点，并拉取其它节点内容。
	if strings.TrimSpace(cfg.RelayURL) != "" {
		relayStop, relayErr := startRelay(cfg, eng)
		if relayErr != nil {
			return relayErr
		}
		defer relayStop()
	}

	// 本地历史页只绑回环地址（spec §9）；起不来不影响同步。
	uiURL, closeUI := startWebUI(eng, cfg.Port, dir, webui.Options{
		Restart: requestRestart,
		Invite:  func() (string, error) { return onboard.Build(cfg).String(), nil },
		Join:    func(code string) error { return joinRoom(dir, code) },
		Status:  eng.Snapshot,
	})
	if closeUI != nil {
		defer closeUI()
	}

	// 全局热键：Windows 在进程内注册，其它平台由桌面快捷键拉 `clipshare pick`。
	// 只在常驻模式启用（--no-tray 多见于无桌面环境）。
	if !cfg.NoTray {
		stopHotkey := startHotkey(eng, cfg, dir)
		defer stopHotkey()
	}

	logStartup(cfg, dir, uiURL, firstRun)

	return serveUntilExit(eng, cfg, dir, uiURL)
}

// resolveConfigDir 决定配置目录：--config-dir 优先，否则系统用户配置目录。
func resolveConfigDir(opts *options) (string, error) {
	if dir := strings.TrimSpace(opts.configDir); dir != "" {
		return dir, nil
	}
	dir, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("定位配置目录失败: %w", err)
	}
	return dir, nil
}

// loadConfig 加载配置、套用 flag 覆盖并校验。
//
// 配置文件损坏时 config.Load 已经备份并重建，这里只告警不退出（spec §10）。
func loadConfig(dir string, opts *options) (*config.Config, error) {
	cfg, err := config.Load(dir)
	if err != nil {
		log.Printf("警告: 加载配置: %v", err)
	}
	if cfg == nil {
		// Load 在「文件读不了」时返回 nil；用默认值继续，保证工具仍能跑。
		cfg = config.Default()
	}

	if opts.room != "" {
		cfg.RoomKey = strings.TrimSpace(opts.room)
	}
	if opts.name != "" {
		cfg.NodeName = opts.name
	}
	if opts.port != 0 {
		cfg.Port = opts.port
	}
	if opts.mdnsSet {
		cfg.MDNS = opts.mdns
	}
	if opts.noTray {
		cfg.NoTray = true
	}
	if opts.noPersist {
		cfg.Persist = false
	}
	for _, seed := range opts.seeds {
		if !slices.Contains(cfg.Seeds, seed) {
			cfg.Seeds = append(cfg.Seeds, seed)
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置无效: %w", err)
	}
	return cfg, nil
}

// startWebUI 在 127.0.0.1 上起本地历史页，返回可打开的地址与关闭函数。
//
// 只绑回环地址（spec §9：本包不鉴权）。历史页不可用不阻断同步：
// 失败时只记日志并返回空地址。这里 listen 的是 port+1，绝不碰同步端口。
func startWebUI(ctrl engine.Controller, syncPort int, cfgDir string, webOpts webui.Options) (string, func()) {
	ln, err := net.Listen("tcp", loopbackAddr(syncPort+uiAddrOffset))
	if err != nil {
		// port+1 被占用时退回系统分配的端口，优先保证页面可用。
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Printf("警告: 本地历史页启动失败（不影响同步）: %v", err)
			return "", nil
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/", webui.Handler(ctrl, webOpts))
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Printf("本地历史页异常退出: %v", serveErr)
		}
	}()

	// 记录实际监听地址：port+1 被占用时会退回随机端口，
	// 而 `clipshare pick` 是独立进程，必须知道真实地址。
	// 记录实际监听地址：port+1 被占用时会退回随机端口，
	// 而 `clipshare pick` 是独立进程，必须知道真实地址。
	// 注意：清理只能放在返回的关闭函数里——在这里 defer 会在函数返回时立刻删掉它。
	addrPath := filepath.Join(cfgDir, uiAddrFile)
	if writeErr := os.WriteFile(addrPath, []byte(ln.Addr().String()), 0o600); writeErr != nil {
		log.Printf("警告: 写入历史页地址文件失败（pick 将回退到 port+1）: %v", writeErr)
	}

	url := "http://" + ln.Addr().String() + "/ui"
	return url, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		_ = os.Remove(addrPath)
	}
}

func loopbackAddr(port int) string {
	if port < 1 || port > 65535 {
		return "127.0.0.1:0"
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// serveUntilExit 在「跑了托盘」与「等信号」两种模式间选择，返回即表示该退出了。
func serveUntilExit(eng *engine.Engine, cfg *config.Config, cfgDir, uiURL string) error {
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if cfg.NoTray {
		if uiURL != "" {
			log.Printf("已禁用托盘（--no-tray）：本地历史页 %s，Ctrl+C 退出", uiURL)
		} else {
			log.Printf("已禁用托盘（--no-tray），Ctrl+C 退出")
		}
		select {
		case sig := <-sigCh:
			log.Printf("收到信号 %v，开始退出…", sig)
		case <-restartCh:
			log.Printf("收到重启请求，正在退出以便重新拉起…")
		}
		return nil // 返回后由 serve 的 defer 停止 engine（历史随之落盘）
	}

	// 托盘跑在主 goroutine 上：systray 要求原生 UI 线程，放 goroutine 里在
	// Windows 上会建出没有消息循环的幽灵图标。信号退出必须由旁路线程收尾，
	// 因为主 goroutine 此刻卡在 tray.Run 里。
	go func() {
		select {
		case sig := <-sigCh:
			log.Printf("收到信号 %v，开始退出…", sig)
			// os.Exit 会跳过 defer，所以这里显式停 engine 让历史落盘。
			eng.Stop()
			os.Exit(0)
		case <-restartCh:
			// 重启也要走「正常退出」这条路：让 tray.Run 返回，serve 的 defer
			// 依次把 engine 停掉、历史落盘、UI 端口释放，再由 run() 重新拉起。
			log.Printf("收到重启请求，正在退出以便重新拉起…")
			tray.Quit()
		}
	}()

	if runTray(eng, cfgDir, uiURL) {
		return nil // 用户点了托盘菜单「退出」
	}

	// 托盘起不来：降级为无托盘模式，继续等信号（spec §10）。
	// 注意这里同样要接受「重启」请求——降级模式下用户仍可能从历史页点重启，
	// 只听信号会让重启按钮石沉大海（实测踩到，Windows 无桌面会话时正是这种形态）。
	select {
	case sig := <-sigCh:
		log.Printf("收到信号 %v，开始退出…", sig)
	case <-restartCh:
		log.Printf("收到重启请求，正在退出以便重新拉起…")
	}
	return nil
}

// runTray 在主 goroutine 上运行托盘；返回 false 表示托盘不可用（已降级）。
func runTray(eng *engine.Engine, cfgDir, uiURL string) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if uiURL != "" {
				log.Printf("警告: 托盘不可用，已降级为无托盘模式（本地历史页 %s）: %v", uiURL, r)
			} else {
				log.Printf("警告: 托盘不可用，已降级为无托盘模式: %v", r)
			}
			ok = false
		}
	}()
	tray.Run(eng, tray.Options{
		UIAddr:    uiURL,
		Restart:   requestRestart,
		ConfigDir: cfgDir,
		Version:   version,
	})
	return true
}

// logStartup 打印启动信息。
//
// 房间码打**完整值**：用户要把它整串复制到另一台机器，只打前缀会让人照着前缀填，
// 结果对端 401（v0.1.0 的回归就是这么来的）。日志里出现完整密钥当然有代价，
// 所以文案里带一句「贴日志求助前请先打码」，README 的安全边界也写了同一句。
func logStartup(cfg *config.Config, cfgDir, uiURL string, firstRun bool) {
	log.Printf("clipshare %s 已启动：节点 %q，端口 %d，房间 %s", version, cfg.NodeName, cfg.Port, model.RoomHash(cfg.RoomKey))
	log.Printf("配置文件: %s", config.Path(cfgDir))
	if uiURL != "" {
		log.Printf("本地历史页: %s", uiURL)
	}
	// 多机连通失败的头号原因是房间码没对齐，所以每次启动都打出来。
	log.Printf("房间码: %s —— 其他机器填同一个 roomKey 才能互通（贴日志求助前请先打码）", cfg.RoomKey)
	if firstRun {
		log.Printf("首次启动：已生成配置 %s；把各台机器的 roomKey 改成同一个值，再分别启动", config.Path(cfgDir))
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
