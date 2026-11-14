package main

import (
	"io"
	"log"
	"os"
	"testing"
	"time"

	"clipshare/internal/config"
)

// 拓扑开关：必须落盘 + 拒绝「三种全关」，并触发一次重启请求。
func TestToggleTransportSavesAndRestarts(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Dir = dir
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}
	restartFlag.Store(false)

	err := toggleTransport(dir, cfg, func(c *config.Config) error {
		c.LANEnabled = false
		c.RelayURL = "https://example.com:8443"
		c.RelayEnabled = true
		return nil
	})
	if err != nil {
		t.Fatalf("合法切换应成功: %v", err)
	}

	loaded, lerr := config.Load(dir)
	if lerr != nil {
		t.Fatalf("落盘后应能重新加载: %v", lerr)
	}
	if loaded.LANEnabled {
		t.Error("lanEnabled=false 未落盘")
	}
	if !loaded.RelayActive() {
		t.Error("中继应处于生效状态")
	}

	// 等重启被触发（实现里有 300ms 的延迟，让菜单先画出勾选）
	deadline := time.Now().Add(2 * time.Second)
	for !restartFlag.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !restartFlag.Load() {
		t.Error("拓扑切换后应触发重启")
	}
	restartFlag.Store(false)
}

func TestToggleTransportRejectsAllOff(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Dir = dir
	if err := cfg.Save(dir); err != nil {
		t.Fatal(err)
	}

	err := toggleTransport(dir, cfg, func(c *config.Config) error {
		c.LANEnabled = false
		c.HubEnabled = false
		c.RelayURL = ""
		return nil
	})
	if err == nil {
		t.Fatal("三种传输全关必须被拒绝")
	}
	// 被拒时不能把配置写坏
	loaded, _ := config.Load(dir)
	if !loaded.LANEnabled {
		t.Error("校验失败时不应改写配置")
	}
}

func TestHubOnlyDetection(t *testing.T) {
	cfg := config.Default()
	if hubOnly(cfg) {
		t.Error("默认配置不是纯中继")
	}
	cfg.LANEnabled = false
	cfg.HubEnabled = true
	if !hubOnly(cfg) {
		t.Error("关局域网 + 开 hub 应为纯中继")
	}
	cfg.RelayURL = "https://x:1"
	cfg.RelayEnabled = true
	if hubOnly(cfg) {
		t.Error("同时做中继客户端时不应判定为纯中继")
	}
}

func TestMain(m *testing.M) {
	logSetOutput(io.Discard)
	os.Exit(m.Run())
}

// logSetOutput 把标准日志改到指定 writer（测试静音用）。
func logSetOutput(w io.Writer) { log.SetOutput(w) }
