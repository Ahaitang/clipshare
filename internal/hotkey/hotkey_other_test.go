//go:build !windows

package hotkey

import (
	"errors"
	"testing"
)

// 非 Windows 侧的行为约定：Start 永远返回 ErrUnsupported，Stop 永远是空操作。
// 这三条是「调用方可以不写平台分支」的前提，所以必须有测试盯着。

func TestStartReturnsErrUnsupported(t *testing.T) {
	called := false
	err := Start(func() { called = true })
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("非 Windows 平台上 Start 应返回 ErrUnsupported，得到 %v", err)
	}
	if called {
		t.Fatalf("本平台没有热键，onPress 不该被调用")
	}
}

func TestStartTwiceDoesNotPanic(t *testing.T) {
	for i := 1; i <= 2; i++ {
		if err := Start(nil); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("第 %d 次 Start 也应返回 ErrUnsupported，得到 %v", i, err)
		}
	}
}

func TestStopWithoutStartDoesNotPanic(t *testing.T) {
	Stop()
	Stop() // 重复 Stop 同样安全
}

func TestErrUnsupportedIsReadable(t *testing.T) {
	if ErrUnsupported == nil || ErrUnsupported.Error() == "" {
		t.Fatalf("ErrUnsupported 必须有可读的错误信息（调用方会直接打印给用户）")
	}
}
