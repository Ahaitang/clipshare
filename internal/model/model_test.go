package model

import (
	"strings"
	"testing"
	"time"
)

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\r\nb", "a\nb"},
		{"a\x00b", "a\x00b"}, // 中间 NUL 不处理
		{"a\x00\x00", "a"},
		{"  keep indent  ", "  keep indent  "}, // 不做 TrimSpace
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestContentHash(t *testing.T) {
	h1 := ContentHash("hello")
	if len(h1) != 64 {
		t.Fatalf("ContentHash 长度 = %d, want 64", len(h1))
	}
	if h1 != ContentHash("hello") {
		t.Fatal("相同内容应得到相同 hash")
	}
	if h1 == ContentHash("hello ") {
		t.Fatal("不同内容不应得到相同 hash")
	}
}

func TestRoomHash(t *testing.T) {
	h := RoomHash("secret")
	if len(h) != 12 {
		t.Fatalf("RoomHash 长度 = %d, want 12", len(h))
	}
	if h != RoomHash("secret") {
		t.Fatal("同密钥应得到同 hash")
	}
	if h == RoomHash("secret2") {
		t.Fatal("不同密钥应得到不同 hash")
	}
	if strings.Contains(h, "secret") {
		t.Fatal("RoomHash 不得包含密钥原文")
	}
}

func TestNewEntryID(t *testing.T) {
	at := time.Unix(1700000000, 0)
	e1 := NewEntry("dev1", "PC1", "content", at)
	if len(e1.ID) != 16 {
		t.Fatalf("ID 长度 = %d, want 16", len(e1.ID))
	}
	if e1.ID != NewEntry("dev1", "PC1", "content", at.Add(time.Hour)).ID {
		t.Fatal("ID 不应依赖时间（否则跨机去重会失效）")
	}
	if e1.ID == NewEntry("dev2", "PC1", "content", at).ID {
		t.Fatal("不同来源节点应有不同 ID")
	}
	if e1.ID == NewEntry("dev1", "PC1", "content2", at).ID {
		t.Fatal("不同内容应有不同 ID")
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"short", 10, "short"},
		{"line1\nline2", 50, "line1 ⏎"},
		{"你好世界", 2, "你好…"},
		{"exactly10!", 10, "exactly10!"},
	}
	for _, c := range cases {
		if got := Truncate(c.in, c.limit); got != c.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
	// 多字节字符不能被截断成半个（否则托盘中会显示乱码）
	if got := Truncate(strings.Repeat("中", 100), 5); got != "中中中中中…" {
		t.Errorf("CJK 截断错误: %q", got)
	}
}
