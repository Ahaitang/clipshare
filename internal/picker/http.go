package picker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"clipshare/internal/model"
)

// HTTPClient 通过本机历史页 API 与**运行中的** clipshare 通信。
//
// `clipshare pick` 是独立进程（由桌面快捷键拉起），拿不到 engine，
// 所以走 loopback HTTP 复用与 Web UI 同一套端点。仅绑回环、无鉴权，
// 与 Web UI 的安全边界一致（spec §9）。
type HTTPClient struct {
	BaseURL string // 形如 http://127.0.0.1:47101
	Client  *http.Client
}

// NewHTTPClient 创建带默认超时的客户端。
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		Client:  &http.Client{Timeout: 3 * time.Second},
	}
}

// Entries 取历史列表（最新在前）。
func (c *HTTPClient) Entries() ([]model.Entry, error) {
	resp, err := c.Client.Get(c.BaseURL + "/ui/api/entries")
	if err != nil {
		return nil, fmt.Errorf("连接本机历史页失败（clipshare 是否在运行？）: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("历史页返回 HTTP %d", resp.StatusCode)
	}
	var entries []model.Entry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("解析历史失败: %w", err)
	}
	return entries, nil
}

// Pull 取回指定条目（S3：写本机剪贴板但不广播）。
func (c *HTTPClient) Pull(id string) error {
	body, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return fmt.Errorf("序列化请求失败: %w", err)
	}
	resp, err := c.Client.Post(c.BaseURL+"/ui/api/pull", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("连接本机历史页失败（clipshare 是否在运行？）: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("取回失败: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
}
