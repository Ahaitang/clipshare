// Package relay 是中继客户端（B/C 侧）：把本机变化上行到中心点，并从中心点拉取其它节点的内容。
//
// 为什么用长轮询而不是 WebSocket：
//   - 只用 HTTP/1.1，穿 NAT / 代理最稳，服务端零额外依赖（标准库）；
//   - 语义简单到可以写确定性测试：一次 GET 要么带着新条目回来，要么挂到超时回空。
//
// 延迟换取的代价是 wait 上限（服务端 5 秒兜底），实际体验是「复制即到」量级。
package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"clipshare/internal/model"
)

// Config 是客户端参数。
type Config struct {
	// BaseURL 是中心点地址，如 https://101.201.30.29:13004（不带结尾斜杠）。
	BaseURL string
	RoomKey string
	// Pin 是中心点证书的 "sha256:<hex>" 指纹；为空则不校验（仅调试用）。
	Pin string
	// IntervalMs 是长轮询挂起时长。
	IntervalMs int
	// SelfDeviceID 用于忽略「自己发出去又被下发回来」的条目。
	SelfDeviceID string
	// OnEntry 收到其它节点的条目时调用（内部会做去重与 apply）。
	OnEntry func(model.Entry)
	Logf    func(format string, args ...any)
}

// Client 是中继客户端。并发安全。
type Client struct {
	cfg  Config
	http *http.Client

	stop    chan struct{}
	wg      sync.WaitGroup
	started atomic.Bool
	// synced 表示「已经取过一次游标」。
	// 不能用 lastSeq == 0 当哨兵：空房间的游标本来就是 0，那样客户端会永远停在
	// 「只取游标」阶段，之后谁都收不到（实测踩到）。
	synced  atomic.Bool
	lastSeq atomic.Uint64
}

// New 构造客户端。
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.IntervalMs <= 0 {
		cfg.IntervalMs = 1000
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Client{cfg: cfg, http: newHTTPClient(cfg.Pin), stop: make(chan struct{})}
}

// Start 启动拉取循环。首次只取游标，避免一上来就被灌一整队历史。
func (c *Client) Start() {
	if !c.started.CompareAndSwap(false, true) {
		return
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.loop()
	}()
}

// Stop 结束循环。
func (c *Client) Stop() {
	if !c.started.CompareAndSwap(true, false) {
		return
	}
	close(c.stop)
	c.wg.Wait()
}

// LastSeq 返回当前游标（排障用）。
func (c *Client) LastSeq() uint64 { return c.lastSeq.Load() }

// Publish 上行一条本机产生的条目。失败即返回错误，由调用方记日志（不重试）。
func (c *Client) Publish(entry model.Entry) error {
	body, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("序列化条目失败: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/v1/items", bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("上行到中心点失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("中心点拒绝上行: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}

type feedItem struct {
	Seq   uint64      `json:"seq"`
	Entry model.Entry `json:"entry"`
}

type feedResp struct {
	Items   []feedItem `json:"items"`
	LastSeq uint64     `json:"lastSeq"`
	Peers   int        `json:"peers"`
	Reset   bool       `json:"reset"`
}

func (c *Client) loop() {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		select {
		case <-c.stop:
			return
		default:
		}

		var (
			resp feedResp
			err  error
		)
		if !c.synced.Load() {
			// 首次只取游标：不申请历史，避免把积压内容一次性灌进剪贴板。
			resp, err = c.fetch(nil, 0)
			if err == nil {
				c.lastSeq.Store(resp.LastSeq)
				c.synced.Store(true)
				c.cfg.Logf("[relay] 已连上中心点，当前游标 %d（%d 个在线节点）", resp.LastSeq, resp.Peers)
				backoff = time.Second
				continue
			}
		} else {
			since := c.lastSeq.Load()
			resp, err = c.fetch(&since, c.cfg.IntervalMs)
			if err == nil {
				backoff = time.Second
				c.handle(resp)
				continue
			}
		}

		c.cfg.Logf("[relay] 拉取失败（%v），%s 后重试", err, backoff)
		select {
		case <-c.stop:
			return
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

func (c *Client) handle(resp feedResp) {
	if resp.Reset {
		// 服务端重启或游标滑出窗口：对齐到最新，不做重放。
		c.lastSeq.Store(resp.LastSeq)
		return
	}
	for _, it := range resp.Items {
		// 自己发出去的条目会被服务端下发回来，必须跳过，否则会自己覆盖自己。
		if it.Entry.Origin != "" && it.Entry.Origin == c.cfg.SelfDeviceID {
			c.lastSeq.Store(it.Seq)
			continue
		}
		if c.cfg.OnEntry != nil {
			c.cfg.OnEntry(it.Entry)
		}
		c.lastSeq.Store(it.Seq)
	}
	// 没有条目时也对齐：服务端重启后序号会回退，这里能自然跟上。
	if len(resp.Items) == 0 && resp.LastSeq < c.lastSeq.Load() {
		c.lastSeq.Store(resp.LastSeq)
	}
}

func (c *Client) fetch(since *uint64, wait int) (feedResp, error) {
	var out feedResp
	url := fmt.Sprintf("%s/v1/feed?wait=%d", c.cfg.BaseURL, wait)
	if since != nil {
		// 必须显式带 since（哪怕是 0）：服务端把「没有 since」等同于「只回游标」，
		// 省略会导致空房间里的第一条条目被永久跳过。
		url += fmt.Sprintf("&since=%d", *since)
	}
	// 长轮询的 HTTP 超时必须比 wait 更长，否则会被自己的超时先掐断。
	timeout := time.Duration(wait)*time.Millisecond + 10*time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, err
	}
	c.setAuth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return out, fmt.Errorf("连接中心点失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return out, fmt.Errorf("中心点返回 HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return out, fmt.Errorf("解析中心点响应失败: %w", err)
	}
	return out, nil
}

func (c *Client) setAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.cfg.RoomKey)
	req.Header.Set("X-Room", model.RoomHash(c.cfg.RoomKey))
}

// newHTTPClient 构造按指纹校验证书的客户端。
//
// 自签证书无法用标准链校验，所以关掉标准校验、改为比对 SHA-256 指纹：
// 只要指纹对得上，中间人就无法冒充中心点（这也是设计文档里 TLS 方案的落点）。
func newHTTPClient(pin string) *http.Client {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			// 自签场景：标准链校验必然失败，安全由下面的指纹比对负责。
			InsecureSkipVerify: true,
			VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
				if strings.TrimSpace(pin) == "" {
					return nil
				}
				if len(rawCerts) == 0 {
					return errors.New("对端没有提供证书")
				}
				sum := sha256.Sum256(rawCerts[0])
				got := "sha256:" + hex.EncodeToString(sum[:])
				if !strings.EqualFold(got, pin) {
					return fmt.Errorf("证书指纹不匹配：期望 %s，实际 %s", pin, got)
				}
				return nil
			},
		},
	}
	return &http.Client{Transport: tr}
}
