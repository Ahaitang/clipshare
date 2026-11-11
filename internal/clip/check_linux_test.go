//go:build linux

package clip

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBin 创建一个只含给定命令的最小 PATH 目录（可执行空脚本）。
func fakeBin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("写入假命令 %s 失败: %v", name, err)
		}
	}
	return dir
}

func TestCheckAvailableX11(t *testing.T) {
	for _, cmd := range []string{"xclip", "xsel"} {
		t.Run(cmd, func(t *testing.T) {
			t.Setenv("PATH", fakeBin(t, cmd))
			t.Setenv("DISPLAY", ":0")
			t.Setenv("WAYLAND_DISPLAY", "")
			if err := CheckAvailable(); err != nil {
				t.Fatalf("%s 存在时自检应通过，实际: %v", cmd, err)
			}
		})
	}
}

func TestCheckAvailableX11MissingTools(t *testing.T) {
	t.Setenv("PATH", fakeBin(t))
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")

	err := CheckAvailable()
	if err == nil {
		t.Fatal("期望自检失败，实际 nil")
	}
	msg := err.Error()
	for _, want := range []string{"xclip", "xsel", "apt install"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息缺少可执行提示 %q: %v", want, err)
		}
	}
}

func TestCheckAvailableWayland(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "wl-paste", "wl-copy"))
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if err := CheckAvailable(); err != nil {
		t.Fatalf("Wayland 命令齐全时自检应通过，实际: %v", err)
	}
}

// Wayland 会话缺 wl-clipboard 时必须报错，而不是回落到 X11 工具
// （那样只会读到一个已经不同步的 XWayland 剪贴板）。
func TestCheckAvailableWaylandMissingWL(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "xclip"))
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")

	err := CheckAvailable()
	if err == nil {
		t.Fatal("期望自检失败，实际 nil")
	}
	msg := err.Error()
	for _, want := range []string{"wl-paste", "wl-clipboard", "WAYLAND_DISPLAY"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息缺少 %q: %v", want, err)
		}
	}
}

func TestCheckAvailableNoGraphicalSession(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "xclip", "wl-paste", "wl-copy"))
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")

	err := CheckAvailable()
	if err == nil {
		t.Fatal("期望自检失败，实际 nil")
	}
	if !strings.Contains(err.Error(), "DISPLAY") {
		t.Fatalf("错误信息应提到 DISPLAY/WAYLAND_DISPLAY: %v", err)
	}
}
