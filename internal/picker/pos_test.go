package picker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPosMissing(t *testing.T) {
	if _, _, ok := LoadPos(filepath.Join(t.TempDir(), "nope")); ok {
		t.Fatal("文件不存在应返回 ok=false")
	}
	if _, _, ok := LoadPos(""); ok {
		t.Fatal("空路径应返回 ok=false")
	}
}

func TestSaveLoadPosRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "picker.pos")
	if err := SavePos(p, 1234, 567); err != nil {
		t.Fatalf("SavePos: %v", err)
	}
	x, y, ok := LoadPos(p)
	if !ok || x != 1234 || y != 567 {
		t.Fatalf("往返失败: %d,%d ok=%v", x, y, ok)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("权限 = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadPosRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	for _, body := range []string{"", "abc", "1", "1,2,3", "1,x", "  ,  "} {
		p := filepath.Join(dir, "p")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, ok := LoadPos(p); ok {
			t.Errorf("非法内容 %q 不应被接受", body)
		}
	}
}

func TestClampPosKeepsWindowOnScreen(t *testing.T) {
	cases := []struct{ x, y, wantX, wantY int }{
		{-100, -50, 0, 0},         // 负坐标拉回原点
		{99999, 99999, 1280, 650}, // 右下越界夹到边界（1920-640, 1080-430）
		{100, 100, 100, 100},      // 合法位置不动
	}
	for _, c := range cases {
		gx, gy := ClampPos(c.x, c.y, 1920, 1080, 640, 430)
		if gx != c.wantX || gy != c.wantY {
			t.Errorf("ClampPos(%d,%d) = (%d,%d), want (%d,%d)", c.x, c.y, gx, gy, c.wantX, c.wantY)
		}
	}
	// 窗口比屏幕还大时不应出现负坐标
	if gx, gy := ClampPos(10, 10, 320, 200, 640, 430); gx != 0 || gy != 0 {
		t.Errorf("小屏幕应夹到 (0,0)，实际 (%d,%d)", gx, gy)
	}
}
