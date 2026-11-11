// Package clip 提供剪贴板读写与变更轮询。
//
// Clipboard 接口把平台实现隔离掉，engine 的单测可以用假剪贴板替换。
// 真实实现基于 github.com/atotto/clipboard：Linux 侧依赖外部命令
// （xclip / xsel，Wayland 下 wl-clipboard），Windows 侧走 Win32 API。
package clip

import (
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/atotto/clipboard"
)

// Clipboard 是剪贴板读写抽象。
type Clipboard interface {
	// Read 返回当前剪贴板文本。空字符串表示「没有文本」（例如复制的是一张图片）。
	Read() (string, error)
	// Write 用给定文本替换剪贴板内容。
	Write(s string) error
}

// ErrUnavailable 表示当前环境没有可用的剪贴板工具。
var ErrUnavailable = errors.New("剪贴板不可用")

// Adapter 是基于 atotto/clipboard 的真实 Clipboard 实现。
//
// 零值可用；它不缓存任何状态，读写每次都落到系统剪贴板。
type Adapter struct{}

// New 返回平台剪贴板实现，并在返回前做启动自检。
//
// 自检失败时返回的错误信息里带有可直接执行的安装命令，
// 由 cmd/clipshare 打印后退出（spec §10：不静默空转）。
func New() (Clipboard, error) {
	if err := CheckAvailable(); err != nil {
		return nil, err
	}
	return Adapter{}, nil
}

// Read 读取剪贴板文本。
func (Adapter) Read() (string, error) {
	var out string
	err := withRetry(func() error {
		var err error
		out, err = readOnce()
		return err
	})
	if err != nil {
		return "", err
	}
	return out, nil
}

// Write 写入剪贴板文本。
func (Adapter) Write(s string) error {
	return withRetry(func() error { return writeOnce(s) })
}

func readOnce() (string, error) {
	if clipboard.Unsupported {
		return "", fmt.Errorf("%w: 未找到可用的剪贴板命令", ErrUnavailable)
	}
	s, err := clipboard.ReadAll()
	if err != nil {
		return mapReadError(err)
	}
	return s, nil
}

func writeOnce(s string) error {
	if clipboard.Unsupported {
		return fmt.Errorf("%w: 未找到可用的剪贴板命令", ErrUnavailable)
	}
	if err := clipboard.WriteAll(s); err != nil {
		return fmt.Errorf("写入剪贴板失败: %w", err)
	}
	return nil
}

// mapReadError 把底层命令错误映射成 Clipboard 接口语义：
//
//   - 命令跑起来了但拿不到文本（空剪贴板、只含图片、selection 无人持有）→ ("", nil)；
//   - 命令本身缺失或其它真实故障 → 返回错误，由调用方决定告警。
//
// 这样轮询中的「复制的是一张图」不会被当成错误刷日志。
func mapReadError(err error) (string, error) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return "", nil
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return "", fmt.Errorf("读取剪贴板失败: %w", err)
}

// withRetry 按平台给定的退避序列重试；retryBackoff 为 nil 时只尝试一次。
func withRetry(fn func() error) error {
	err := fn()
	for _, d := range retryBackoff {
		if err == nil {
			return nil
		}
		time.Sleep(d)
		err = fn()
	}
	return err
}
