//go:build windows

package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

// TestIconBytesIsICO 断言 Windows 上交给 systray 的是 ICO（LoadImage 只认 ICO）。
func TestIconBytesIsICO(t *testing.T) {
	for _, state := range []iconState{iconNormal, iconPaused, iconIdle} {
		data := iconBytes(state)
		if bytes.HasPrefix(data, []byte("\x89PNG")) {
			t.Fatalf("state %v 直接给了 PNG，Windows 会加载失败", state)
		}
		if len(data) < 22 {
			t.Fatalf("state %v ICO 太短: %d 字节", state, len(data))
		}
		le := binary.LittleEndian
		if le.Uint16(data[2:]) != 1 || le.Uint16(data[4:]) != 1 {
			t.Fatalf("state %v ICO 头不对", state)
		}
		inner := data[22:]
		if _, err := png.Decode(bytes.NewReader(inner)); err != nil {
			t.Fatalf("state %v ICO 内嵌的不是合法 PNG: %v", state, err)
		}
	}
}
