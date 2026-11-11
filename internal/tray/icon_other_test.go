//go:build !windows

package tray

import (
	"bytes"
	"testing"
)

// TestIconBytesIsPNG 断言非 Windows 平台直接使用 PNG（systray 用 image/png 解码）。
func TestIconBytesIsPNG(t *testing.T) {
	for _, state := range []iconState{iconNormal, iconPaused, iconIdle} {
		if data := iconBytes(state); !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("state %v 应以 PNG 形式交给 systray", state)
		}
	}
}
