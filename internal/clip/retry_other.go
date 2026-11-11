//go:build !windows

package clip

import "time"

// retryBackoff 在非 Windows 平台为空：xclip/xsel/wl-paste 没有 OpenClipboard
// 那种「剪贴板被别的进程独占」的问题，失败直接交给调用方。
var retryBackoff []time.Duration
