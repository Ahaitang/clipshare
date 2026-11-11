package clip

import (
	"errors"
	"os/exec"
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
