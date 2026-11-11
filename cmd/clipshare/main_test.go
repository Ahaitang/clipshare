package main

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"clipshare/internal/config"
)

// TestParseOptionsDefaults：不带 flag 时所有覆盖项都是「未指定」。
func TestParseOptionsDefaults(t *testing.T) {
	var out, errOut bytes.Buffer
	opts, err := parseOptions(nil, &out, &errOut)
	if err != nil {
		t.Fatalf("解析空参数失败: %v", err)
	}
	if opts.room != "" || opts.port != 0 || opts.name != "" || opts.configDir != "" {
		t.Fatalf("默认值应为空: %+v", opts)
	}
	if len(opts.seeds) != 0 || opts.noTray || opts.noPersist || opts.showVersion {
		t.Fatalf("默认值应为零值: %+v", opts)
	}
	if !opts.mdns || opts.mdnsSet {
		t.Fatalf("mdns 默认应为 true 且视为未显式指定: %+v", opts)
	}
}

// TestParseOptionsAllFlags：任务要求的 9 个 flag 都能解析。
func TestParseOptionsAllFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	args := []string{
		"--room", "abc123",
		"--port", "47999",
		"--name", "MYPC",
		"--seed", "192.168.1.7:47100",
		"--seed", "192.168.1.8:47100",
		"--no-tray",
		"--no-persist",
		"--mdns=false",
		"--config-dir", "/tmp/clipshare-test",
	}
	opts, err := parseOptions(args, &out, &errOut)
	if err != nil {
		t.Fatalf("解析失败: %v（stderr: %s）", err, errOut.String())
	}
	if opts.room != "abc123" || opts.port != 47999 || opts.name != "MYPC" {
		t.Fatalf("基础字段不对: %+v", opts)
	}
	if len(opts.seeds) != 2 || opts.seeds[0] != "192.168.1.7:47100" {
		t.Fatalf("--seed 应可重复: %+v", opts.seeds)
	}
	if !opts.noTray || !opts.noPersist {
		t.Fatalf("布尔开关未生效: %+v", opts)
	}
	if opts.mdns || !opts.mdnsSet {
		t.Fatalf("--mdns=false 应关闭 mdns 且标记为显式指定: %+v", opts)
	}
	if opts.configDir != "/tmp/clipshare-test" {
		t.Fatalf("--config-dir = %q", opts.configDir)
	}
}

// TestParseOptionsVersion：--version 走独立路径，不进入 serve。
func TestParseOptionsVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	opts, err := parseOptions([]string{"--version"}, &out, &errOut)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !opts.showVersion {
		t.Fatal("--version 未置位")
	}
}

// TestRunVersion：--version 打印 main.version（可被 ldflags 覆盖的那个变量）。
func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("退出码 = %d，stderr: %s", code, errOut.String())
	}
	want := "clipshare " + version
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("输出 = %q，期望 %q", got, want)
	}
}

// TestRunHelp：--help 打印选项并以 0 退出（flag.ErrHelp 不算错误）。
func TestRunHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"--help"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("--help 退出码 = %d", code)
	}
	usage := errOut.String()
	for _, flagName := range []string{"-room", "-port", "-name", "-seed", "-no-tray", "-no-persist", "-mdns", "-config-dir", "-version"} {
		if !strings.Contains(usage, flagName) {
			t.Fatalf("--help 缺少 %s:\n%s", flagName, usage)
		}
	}
}

// TestRunBadFlag：未知 flag 退出码 2。
func TestRunBadFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--nope"}, &out, &errOut); code != 2 {
		t.Fatalf("未知 flag 退出码 = %d，期望 2", code)
	}
}

// TestRunExtraArgs：多余的位置参数也按用法错误处理。
func TestRunExtraArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"extra"}, &out, &errOut); code != 2 {
		t.Fatalf("多余参数退出码 = %d，期望 2", code)
	}
	if !strings.Contains(errOut.String(), "多余的参数") {
		t.Fatalf("stderr = %q", errOut.String())
	}
}

// TestLoadConfigOverridesFlags：flag 优先级高于配置文件，且不写回磁盘。
func TestLoadConfigOverridesFlags(t *testing.T) {
	dir := t.TempDir()
	// 先落一份配置，模拟「已跑过一次」。
	base := config.Default()
	base.RoomKey = "config-room"
	base.Port = 47100
	base.MDNS = true
	base.Seeds = []string{"10.0.0.1:47100"}
	if err := base.Save(dir); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	opts := &options{
		room:      "flag-room",
		port:      47999,
		name:      "FLAG-NAME",
		seeds:     multiFlag{"10.0.0.1:47100", "10.0.0.2:47100"},
		noTray:    true,
		noPersist: true,
		mdns:      false,
		mdnsSet:   true,
	}
	cfg, err := loadConfig(dir, opts)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.RoomKey != "flag-room" || cfg.Port != 47999 || cfg.NodeName != "FLAG-NAME" {
		t.Fatalf("flag 未覆盖配置: %+v", cfg)
	}
	if cfg.MDNS || !cfg.NoTray || cfg.Persist {
		t.Fatalf("布尔覆盖不对: %+v", cfg)
	}
	// 已存在的 seed 不重复追加，新 seed 追加到后面。
	if len(cfg.Seeds) != 2 || cfg.Seeds[0] != "10.0.0.1:47100" || cfg.Seeds[1] != "10.0.0.2:47100" {
		t.Fatalf("seeds = %v", cfg.Seeds)
	}

	// flag 只影响本次运行，不落回配置文件。
	reloaded, err := config.Load(dir)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	if reloaded.RoomKey != "config-room" || reloaded.Port != 47100 {
		t.Fatalf("flag 覆盖被写回了配置文件: %+v", reloaded)
	}
}

// TestLoadConfigInvalidFlagValues：非法覆盖值在启动前就被拦下，报错可读。
func TestLoadConfigInvalidFlagValues(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		opts *options
		want string
	}{
		{"端口越界", &options{port: 70000}, "port"},
		{"seed 格式错误", &options{seeds: multiFlag{"192.168.1.7"}}, "seeds 里的地址"},
		{"房间码为空", &options{room: "   "}, "roomKey"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadConfig(dir, tc.opts)
			if err == nil {
				t.Fatal("期望报错，实际 nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息 %q 未包含 %q", err, tc.want)
			}
		})
	}
}

// TestLogStartupPrintsFullRoomKey 是 t13 的回归保护：
// 启动日志曾只打印房间码前 6 位，用户照着日志填到另一台机器必然 401。
// 现在必须逐字符打完整值（旧 6 位 key 也照原样打）。
func TestLogStartupPrintsFullRoomKey(t *testing.T) {
	for _, key := range []string{"bgf3b69dg225", "abc123"} { // 12 位新 key / 6 位旧 key
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			oldOut, oldFlags := log.Writer(), log.Flags()
			log.SetOutput(&buf)
			log.SetFlags(0)
			t.Cleanup(func() {
				log.SetOutput(oldOut)
				log.SetFlags(oldFlags)
			})

			cfg := config.Default()
			cfg.RoomKey = key
			logStartup(cfg, "/tmp/clipshare-test", "http://127.0.0.1:47101/ui", true)

			out := buf.String()
			// 整串出现，且后面紧跟分隔符——截断成前缀时这里匹配不上。
			if want := "房间码: " + key + " ——"; !strings.Contains(out, want) {
				t.Fatalf("启动日志没有完整房间码 %q，实际日志:\n%s", key, out)
			}
			// 首启还应给出配置文件路径，方便用户去复制完整 key。
			if !strings.Contains(out, "首次启动：已生成配置 /tmp/clipshare-test/config.json") {
				t.Fatalf("首次启动日志缺少配置路径:\n%s", out)
			}
		})
	}
}
