package clip

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
)

// TestMapReadError 覆盖「读不到文本」与「真实故障」的分流：
// 前者必须变成 ("", nil)，否则复制图片（空剪贴板）会在轮询里刷错误。
func TestMapReadError(t *testing.T) {
	t.Run("退出码非零视为没有文本", func(t *testing.T) {
		got, err := mapReadError(&exec.ExitError{})
		if err != nil {
			t.Fatalf("期望 nil 错误（空剪贴板），实际 %v", err)
		}
		if got != "" {
			t.Fatalf("期望空字符串，实际 %q", got)
		}
	})

	t.Run("命令缺失返回 ErrUnavailable", func(t *testing.T) {
		_, err := mapReadError(&exec.Error{Name: "xclip", Err: exec.ErrNotFound})
		if err == nil {
			t.Fatal("期望错误，实际 nil")
		}
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("期望包装 ErrUnavailable，实际 %v", err)
		}
	})

	t.Run("其它错误保留原始信息", func(t *testing.T) {
		boom := errors.New("pipe closed")
		_, err := mapReadError(boom)
		if err == nil {
			t.Fatal("期望错误，实际 nil")
		}
		if !errors.Is(err, boom) {
			t.Fatalf("期望包装原始错误，实际 %v", err)
		}
	})
}

func TestNewReturnsClipboardWhenToolsPresent(t *testing.T) {
	if err := CheckAvailable(); err != nil {
		t.Skipf("当前环境剪贴板不可用，跳过: %v", err)
	}
	c, err := New()
	if err != nil {
		t.Fatalf("CheckAvailable 通过时 New 不应失败: %v", err)
	}
	if c == nil {
		t.Fatal("New 返回了 nil Clipboard")
	}
}

// TestWithRetryNoBackoff 在非 Windows 平台（retryBackoff 为空）只尝试一次。
func TestWithRetryNoBackoff(t *testing.T) {
	if len(retryBackoff) != 0 {
		t.Skip("该平台有重试策略，此用例只验证单次尝试")
	}
	calls := 0
	err := withRetry(func() error {
		calls++
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("期望错误，实际 nil")
	}
	if calls != 1 {
		t.Fatalf("期望只调用 1 次，实际 %d 次", calls)
	}
}

// TestMapReadErrorNoTextOnWindows：Windows 上 atotto 用 syscall.Errno(0) 表示
// 「剪贴板里没有文本」——它的错误文本是 "The operation completed successfully"，
// 但作为 error 接口值非 nil。这必须和 Linux 的 xsel 非零退出码一样映射成 ("", nil)，
// 否则轮询会把「空剪贴板」当成真实故障，基线建不起来（用户第一次复制被吞）。
//
// 这条用 Linux 也能跑：syscall.Errno(0) 是跨平台的类型，errors.Is 判定与 OS 无关。
func TestMapReadErrorNoTextOnWindows(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"裸 Errno(0)", syscall.Errno(0)},
		{"被包装的 Errno(0)", fmt.Errorf("读取剪贴板失败: %w", syscall.Errno(0))},
		{"Linux 的 ExitError", &exec.ExitError{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mapReadError(tc.err)
			if err != nil {
				t.Fatalf("「没有文本」不应返回错误，实际 %v", err)
			}
			if got != "" {
				t.Fatalf("「没有文本」应返回空串，实际 %q", got)
			}
		})
	}
}

// TestMapReadErrorKeepsRealFailures：NoText 的放宽不能把真实故障也吞掉。
func TestMapReadErrorKeepsRealFailures(t *testing.T) {
	unavailable := fmt.Errorf("%w: 未找到可用的剪贴板命令", ErrUnavailable)
	if _, err := mapReadError(unavailable); err == nil {
		t.Fatal("ErrUnavailable 必须保持为错误")
	} else if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("应保留 ErrUnavailable，实际 %v", err)
	}

	boom := errors.New("pipe closed")
	if _, err := mapReadError(boom); err == nil {
		t.Fatal("未知错误必须保持为错误")
	}
}
