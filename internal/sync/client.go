package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"clipshare/internal/model"
)

// DefaultTimeout 是单次 POST 的默认超时（spec §6：3s）。
const DefaultTimeout = 3 * time.Second

// Peer 是一个对等节点。
type Peer struct {
	NodeID  string `json:"nodeId"`
	Name    string `json:"name"`
	BaseURL string `json:"baseUrl"`
}

// Label 返回日志友好的节点称呼，例如 "MYPC(abcd1234@http://192.168.1.7:47100)"。
func (p Peer) Label() string {
	id := p.NodeID
	if len(id) > 8 {
		id = id[:8]
	}
	name := p.Name
	if name == "" {
		name = "peer"
	}
	if id == "" {
		return fmt.Sprintf("%s(%s)", name, p.BaseURL)
	}
	return fmt.Sprintf("%s(%s@%s)", name, id, p.BaseURL)
}

// PostError 是一次发送失败的结构化错误：广播结果里靠它区分
// 「对端拒收（有状态码）」和「连不上/超时（Status == 0）」。
type PostError struct {
	Peer   Peer
	Status int
	Err    error
}

func (e *PostError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("发送到 %s 失败: HTTP %d: %v", e.Peer.Label(), e.Status, e.Err)
	}
	return fmt.Sprintf("发送到 %s 失败: %v", e.Peer.Label(), e.Err)
}

func (e *PostError) Unwrap() error { return e.Err }

// Client 是发送端。零值可用（Timeout 走 DefaultTimeout）。
type Client struct {
	// Timeout 是单次 POST 的超时；<=0 时用 DefaultTimeout。
	Timeout time.Duration
	// HTTPClient 可注入自定义客户端（测试或将来加 TLS）；nil 时用带连接池的包级客户端。
	HTTPClient *http.Client
}

// httpClient 返回包级共享客户端，保留连接复用；超时靠 context 控制。
var defaultHTTPClient = &http.Client{}

func (c *Client) timeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultHTTPClient
}

// Post 把一条条目发送到 peer 的 /v1/items。
//
// 不重试（spec S7：剪贴板内容有过期语义，重发会造成时序错乱），
// 失败只通过返回值告知调用方。
func (c *Client) Post(ctx context.Context, p Peer, roomKey string, e model.Entry) error {
	_, err := c.post(ctx, p, roomKey, e)
	return err
}

// post 与 Post 行为一致，但额外返回对端 HTTP 状态码（失败时为 0），
// 供 Broadcaster 在 Result 里带上状态码。
func (c *Client) post(ctx context.Context, p Peer, roomKey string, e model.Entry) (int, error) {
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		return 0, &PostError{Peer: p, Err: errors.New("对端没有 BaseURL")}
	}
	if roomKey == "" {
		return 0, &PostError{Peer: p, Err: errors.New("roomKey 为空，无法构造鉴权头")}
	}

	body, err := json.Marshal(e)
	if err != nil {
		return 0, &PostError{Peer: p, Err: fmt.Errorf("序列化条目失败: %w", err)}
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/items", bytes.NewReader(body))
	if err != nil {
		return 0, &PostError{Peer: p, Err: fmt.Errorf("构造请求失败: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+roomKey)
	req.Header.Set("X-Room", model.RoomHash(roomKey))

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, &PostError{Peer: p, Err: err}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		msg := strings.TrimSpace(string(snippet))
		if msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return resp.StatusCode, &PostError{Peer: p, Status: resp.StatusCode, Err: errors.New(msg)}
	}
	return resp.StatusCode, nil
}
