//go:build windows

package clip

import "time"

// retryBackoff 是 Windows 上剪贴板被占用时的退避序列（spec §10：
// OpenClipboard 失败重试 3 次、20/60/150ms，然后跳过本轮，不刷错误日志）。
var retryBackoff = []time.Duration{
	20 * time.Millisecond,
	60 * time.Millisecond,
	150 * time.Millisecond,
}
