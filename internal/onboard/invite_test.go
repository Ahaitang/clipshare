package onboard

import (
	"strings"
	"testing"

	"clipshare/internal/config"
)

func TestParseRoundTrip(t *testing.T) {
	cases := []string{
		"clipshare1:abc123",
		"clipshare1:abc123@192.168.1.7:47100",
		"clipshare1:abc123@192.168.1.7:47100,10.0.0.5:47100",
	}
	for _, c := range cases {
		inv, err := Parse(c)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c, err)
		}
		if got := inv.String(); got != c {
			t.Errorf("往返不一致: %q → %q", c, got)
		}
	}
}

func TestParseRejects(t *testing.T) {
	bad := []string{
		"",
		"abc123",
		"clipshare1:",
		"clipshare1:@192.168.1.7:47100",
		"clipshare1:abc 123",
		"clipshare1:abc@192.168.1.7",
		"clipshare1:abc@192.168.1.7:0",
		"clipshare1:abc@192.168.1.7:70000",
		"clipshare1:abc@:47100",
	}
	for _, c := range bad {
		if _, err := Parse(c); err == nil {
			t.Errorf("应拒绝 %q", c)
		}
	}
}

func TestParseToleratesWhitespace(t *testing.T) {
	inv, err := Parse("  clipshare1:roomkey  ")
	if err != nil || inv.RoomKey != "roomkey" {
		t.Fatalf("应容忍两侧空白: %+v err=%v", inv, err)
	}
}

func TestParseCaseInsensitivePrefix(t *testing.T) {
	if _, err := Parse("CLIPSHARE1:abc"); err != nil {
		t.Fatalf("前缀应大小写不敏感: %v", err)
	}
}

func TestApplyReplacesSeeds(t *testing.T) {
	cfg := &config.Config{RoomKey: "old", Seeds: []string{"9.9.9.9:47100"}}
	Apply(cfg, Invite{RoomKey: "new", Seeds: []string{"1.2.3.4:47100"}})
	if cfg.RoomKey != "new" {
		t.Errorf("房间码未替换: %q", cfg.RoomKey)
	}
	if len(cfg.Seeds) != 1 || cfg.Seeds[0] != "1.2.3.4:47100" {
		t.Errorf("seeds 应被替换而不是追加: %v", cfg.Seeds)
	}
}

func TestApplyKeepsSeedsWhenInviteHasNone(t *testing.T) {
	cfg := &config.Config{RoomKey: "old", Seeds: []string{"9.9.9.9:47100"}}
	Apply(cfg, Invite{RoomKey: "new"})
	if len(cfg.Seeds) != 1 {
		t.Errorf("邀请码没带地址时不应清空已有 seeds: %v", cfg.Seeds)
	}
}

func TestBuildIncludesPort(t *testing.T) {
	cfg := &config.Config{RoomKey: "room123", Port: 47100}
	code := Build(cfg).String()
	if !strings.HasPrefix(code, Prefix+"room123") {
		t.Fatalf("邀请码前缀不对: %q", code)
	}
}
