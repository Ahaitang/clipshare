//go:build !windows

// Package hotkey 注册「弹出剪贴板历史」的全局热键（Ctrl+Alt+V）。
//
// 本文件是非 Windows 平台的实现：这些平台没有 Win32 的 RegisterHotKey，
// 所以 Start 直接返回 ErrUnsupported，快捷键由桌面环境负责（把快捷键绑到
// `clipshare pick`，见 README）。保留同一个 API 是为了让调用方不必在 cmd 层
// 写平台分支。
//
// 用法与 Windows 侧一致：在独立 goroutine 里调 Start（它会阻塞到 Stop），
// 主 goroutine 继续跑别的事：
//
//	go func() {
//		if err := hotkey.Start(onPress); errors.Is(err, hotkey.ErrUnsupported) {
//			log.Printf("本平台不支持全局热键，请用桌面快捷键调 clipshare pick")
//		}
//	}()
//	...
//	hotkey.Stop()
package hotkey

import "errors"

// ErrUnsupported 表示当前平台不支持全局热键。
//
// 注意：本变量在 hotkey_windows.go 与 hotkey_other.go 里各声明一份，
// 靠构建标签二选一编译；改动时两处必须保持一致。
var ErrUnsupported = errors.New("当前平台不支持全局热键")

// Start 在非 Windows 平台上总是返回 ErrUnsupported。
//
// 它保留了与 Windows 侧相同的签名，因此调用方可以按「本平台没有这个能力」
// 分支降级，而不是把错误当成故障。onPress 永远不会被调用。
func Start(onPress func()) error {
	// 明确丢弃：这些平台不注册热键，回调没有触发时机。
	_ = onPress
	return ErrUnsupported
}

// Stop 在非 Windows 平台上什么也不做：从来没有注册过热键，
// 因此「未 Start 就 Stop」「重复 Stop」都天然是空操作，也不会 panic。
func Stop() {}
