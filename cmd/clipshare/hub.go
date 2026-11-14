package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"clipshare/internal/config"
	"clipshare/internal/hub"
	"clipshare/internal/model"
	"clipshare/internal/relay"
	"clipshare/internal/tlscert"
)

// startHub 启动中继服务端角色（中心点 A）。
//
// 默认走 TLS：即使监听在回环地址上也不例外——因为它通常经内网穿透暴露到公网，
// 「回环」不等于「私有」。自签证书在配置目录里自动生成，指纹由调用方打印给用户。
func startHub(cfg *config.Config, dir string, apply func(model.Entry)) (stop func(), err error) {
	hosts := []string{}
	if host, _, splitErr := net.SplitHostPort(cfg.HubListen); splitErr == nil {
		if host != "" && host != "0.0.0.0" && host != "::" {
			hosts = append(hosts, host)
		}
	}

	var certPEM, keyPEM []byte
	plaintext := cfg.HubAllowPlaintext
	if !plaintext {
		certPEM, keyPEM, err = tlscert.Ensure(cfg.HubCertFile, cfg.HubKeyFile, dir, hosts)
		if err != nil {
			return nil, fmt.Errorf("准备中继证书失败: %w", err)
		}
		fp, fpErr := tlscert.Fingerprint(certPEM)
		if fpErr == nil {
			logPrintf("[hub] 证书指纹（填到客户端的 relayPin）：%s", fp)
		} else {
			logPrintf("[hub] 计算证书指纹失败: %v", fpErr)
		}
	} else {
		logPrintf("[hub] 警告：hubAllowPlaintext 已开启，中继明文监听，仅限内网调试！")
	}

	h := hub.New(hub.Config{
		RoomKey:    cfg.RoomKey,
		RoomHash:   cfg.RoomHashValue(),
		NodeID:     cfg.DeviceID,
		NodeName:   cfg.NodeName,
		Version:    version,
		Keep:       cfg.HubKeep,
		MaxBytes:   cfg.ItemMaxBytes,
		RatePerMin: 120,
		OnEntry:    apply,
		Logf:       logPrintf,
	})

	ln, err := net.Listen("tcp", cfg.HubListen)
	if err != nil {
		return nil, fmt.Errorf("中继监听 %s 失败（端口被占用？）: %w", cfg.HubListen, err)
	}
	srv := &http.Server{Handler: h.Handler(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if plaintext {
			if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
				logPrintf("[hub] 中继服务异常退出: %v", serveErr)
			}
			return
		}
		cert, certErr := tls.X509KeyPair(certPEM, keyPEM)
		if certErr != nil {
			logPrintf("[hub] 证书装载失败: %v", certErr)
			return
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if serveErr := srv.ServeTLS(ln, "", ""); serveErr != nil && serveErr != http.ErrServerClosed {
			logPrintf("[hub] 中继服务异常退出: %v", serveErr)
		}
	}()
	scheme := "https"
	if plaintext {
		scheme = "http"
	}
	logPrintf("[hub] 中继已监听 %s（%s，房间 %s，保留 %d 条）", cfg.HubListen, scheme, cfg.RoomHashValue(), cfg.HubKeep)

	return func() {
		_ = srv.Close()
		<-done
	}, nil
}

// startRelay 启动中继客户端角色（B/C）：把本机变化上行到中心点，并拉取其它节点内容。
func startRelay(cfg *config.Config, eng publishable) (client *relay.Client, stop func(), err error) {
	client = relay.New(relay.Config{
		BaseURL:      cfg.RelayURL,
		RoomKey:      cfg.RoomKey,
		Pin:          cfg.RelayPin,
		IntervalMs:   cfg.RelayIntervalMs,
		SelfDeviceID: cfg.DeviceID,
		OnEntry:      func(e model.Entry) { eng.IngestRemote(e) },
		Logf:         logPrintf,
	})
	eng.AddPublisher(client.Publish)
	client.Start()
	logPrintf("[relay] 已连接中心点 %s（指纹校验：%v）", cfg.RelayURL, strings.TrimSpace(cfg.RelayPin) != "")
	return client, client.Stop, nil
}

// publishable 是 startRelay 需要的最小引擎能力（便于测试替身）。
type publishable interface {
	AddPublisher(func(model.Entry) error)
	IngestRemote(model.Entry) bool
}

// runHubInfo 打印中继配置与实时状态，供部署后自检。
func runHubInfo(args []string, stdout, stderr io.Writer) int {
	dir, cfg, rc := commonFlags("clipshare hub-info", args, stderr)
	if rc != -1 {
		return rc
	}
	if !cfg.HubEnabled {
		fmt.Fprintf(stdout, "中继服务端未启用（hubEnabled=false）。\n")
		fmt.Fprintf(stdout, "启用方式：在 %s 里把 \"hubEnabled\" 改为 true，并设置 hubListen。\n", config.Path(dir))
		return 0
	}
	fmt.Fprintf(stdout, "中继监听：%s\n", cfg.HubListen)
	fmt.Fprintf(stdout, "房间标识：%s\n", cfg.RoomHashValue())
	fmt.Fprintf(stdout, "保留条数：%d\n", cfg.HubKeep)
	fmt.Fprintf(stdout, "明文模式：%v（true 仅限内网调试）\n", cfg.HubAllowPlaintext)

	certFile := cfg.HubCertFile
	if certFile == "" {
		certFile = filepath.Join(dir, "hub-cert.pem")
	}
	if pem, err := readFile(certFile); err == nil {
		if fp, fpErr := tlscert.Fingerprint(pem); fpErr == nil {
			fmt.Fprintf(stdout, "证书指纹：%s\n", fp)
			fmt.Fprintf(stdout, "  ↑ 填到 B/C 的 relayPin\n")
		}
	} else {
		fmt.Fprintf(stdout, "证书指纹：（证书尚未生成，首次启动后可用）\n")
	}

	// 试着连一下自己，把在线节点数与最新序号报出来。
	url := hubSelfURL(cfg)
	client := &http.Client{
		Timeout:   4 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}
	resp, err := client.Get(url + "/v1/health")
	if err != nil {
		fmt.Fprintf(stdout, "实时状态：连不上（%v）——中继是否在运行？\n", err)
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	var st struct {
		Role    string `json:"role"`
		Peers   int    `json:"peers"`
		LastSeq uint64 `json:"lastSeq"`
		Room    string `json:"roomHash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		fmt.Fprintf(stdout, "实时状态：响应解析失败（%v）\n", err)
		return 0
	}
	fmt.Fprintf(stdout, "实时状态：在线节点 %d，最新序号 %d，房间 %s\n", st.Peers, st.LastSeq, st.Room)
	if st.Room != cfg.RoomHashValue() {
		fmt.Fprintf(stdout, "警告：运行中的中继房间（%s）与配置（%s）不一致，可能需要重启。\n", st.Room, cfg.RoomHashValue())
	}
	return 0
}

// hubSelfURL 把监听地址变成本机可访问的 URL（":8443" → "https://127.0.0.1:8443"）。
func hubSelfURL(cfg *config.Config) string {
	host, port, err := net.SplitHostPort(cfg.HubListen)
	if err != nil {
		return "http://127.0.0.1"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "https"
	if cfg.HubAllowPlaintext {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s", scheme, net.JoinHostPort(host, port))
}

// readFile 是 os.ReadFile 的薄封装（单独抽出来便于在测试里替换）。
var readFile = os.ReadFile

// logPrintf 由 serve() 绑定到标准日志，便于测试静音。
var logPrintf = func(format string, args ...any) { log.Printf(format, args...) }

// toggleTransport 修改拓扑开关：写配置 → 校验 → 落盘 → 触发重启。
//
// 为什么必须重启而不是热切换：局域网角色要重建 HTTP 监听与发现层、中继角色要重建
// 长轮询与上行通道，热切换需要在引擎内部维护一堆「可启动/停止的传输」状态机，
// 出错面远大于收益。重启在 Linux 是 exec（PID 不变），在 Windows 约 2 秒。
func toggleTransport(dir string, cfg *config.Config, mutate func(*config.Config) error) error {
	if err := mutate(cfg); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := cfg.Save(dir); err != nil {
		return err
	}
	logPrintf("[tray] 拓扑开关已变更（局域网=%v 中继=%v），正在重启生效",
		cfg.LANEnabled, cfg.RelayActive())
	// 稍后再触发：让菜单能先把勾选画出来，避免用户以为没点动。
	go func() {
		time.Sleep(300 * time.Millisecond)
		requestRestart()
	}()
	return nil
}
