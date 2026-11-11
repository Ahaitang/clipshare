package tray

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"strings"
	"testing"

	"clipshare/internal/engine"
	"clipshare/internal/model"
)

func TestTitleText(t *testing.T) {
	cases := []struct {
		name string
		snap engine.Snapshot
		want string
	}{
		{"完整", engine.Snapshot{NodeName: "书房-MacBook", RoomHash: "a1b2c3d4e5f6"}, "clipshare · 书房-MacBook · a1b2c3"},
		{"无房间码", engine.Snapshot{NodeName: "办公台式"}, "clipshare · 办公台式"},
		{"未设节点名", engine.Snapshot{RoomHash: "abcdef123456"}, "clipshare · clipshare · abcdef"},
		{"短房间码", engine.Snapshot{NodeName: "n", RoomHash: "abc"}, "clipshare · n · abc"},
		{"超长节点名截断", engine.Snapshot{NodeName: strings.Repeat("很", 40)}, "clipshare · " + strings.Repeat("很", 24) + "…"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := titleText(tc.snap); got != tc.want {
				t.Fatalf("titleText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusText(t *testing.T) {
	cases := []struct {
		name string
		snap engine.Snapshot
		want string
	}{
		{"暂停优先", engine.Snapshot{Paused: true, Peers: 3}, "已暂停"},
		{"暂停且无节点", engine.Snapshot{Paused: true}, "已暂停"},
		{"无节点", engine.Snapshot{Peers: 0}, "无可用节点"},
		{"单节点", engine.Snapshot{Peers: 1}, "与 1 台同步中"},
		{"多节点", engine.Snapshot{Peers: 7}, "与 7 台同步中"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := statusText(tc.snap); got != tc.want {
				t.Fatalf("statusText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTooltipText(t *testing.T) {
	snap := engine.Snapshot{Peers: 2, LastError: "连接 dev-a 超时"}
	got := tooltipText("0.1.0", snap)
	for _, want := range []string{"clipshare 0.1.0", "与 2 台同步中", "连接 dev-a 超时"} {
		if !strings.Contains(got, want) {
			t.Fatalf("tooltipText = %q，缺少 %q", got, want)
		}
	}
	if got := tooltipText("", engine.Snapshot{Peers: 1}); strings.Contains(got, "·  ·") {
		t.Fatalf("空版本号不该留下空段: %q", got)
	}
}

func TestSlotTitle(t *testing.T) {
	cases := []struct {
		name  string
		entry model.Entry
		want  string
	}{
		{"本机", model.Entry{Content: "hello"}, "[本机] hello"},
		{"远端有名", model.Entry{Origin: "dev-a", OriginName: "A-PC", Content: "hi"}, "[A-PC] hi"},
		{"远端无名", model.Entry{Origin: "dev-a", Content: "hi"}, "[远端] hi"},
		{"换行单行化", model.Entry{Content: "第一行\n第二行"}, "[本机] 第一行 ⏎"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slotTitle(tc.entry); got != tc.want {
				t.Fatalf("slotTitle = %q, want %q", got, tc.want)
			}
		})
	}

	long := model.Entry{Content: strings.Repeat("剪", slotTitleLimit+10)}
	got := slotTitle(long)
	if !strings.HasPrefix(got, "[本机] ") || !strings.HasSuffix(got, "…") {
		t.Fatalf("超长内容应截断并带省略号: %q", got)
	}
	wantRunes := len([]rune("[本机] ")) + slotTitleLimit + 1 // 标签 + 截断内容 + 省略号
	if n := len([]rune(got)); n != wantRunes {
		t.Fatalf("截断后字符数 = %d, want %d", n, wantRunes)
	}
}

func TestSlotTooltipIsSingleLine(t *testing.T) {
	e := model.Entry{Content: "a\nb\nc"}
	if got := slotTooltip(e); strings.Contains(got, "\n") {
		t.Fatalf("tooltip 不应含换行: %q", got)
	}
}

func TestIconStateFor(t *testing.T) {
	cases := []struct {
		name string
		snap engine.Snapshot
		want iconState
	}{
		{"暂停", engine.Snapshot{Paused: true, Peers: 2}, iconPaused},
		{"无节点", engine.Snapshot{Peers: 0}, iconIdle},
		{"正常", engine.Snapshot{Peers: 2}, iconNormal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := iconStateFor(tc.snap); got != tc.want {
				t.Fatalf("iconStateFor = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRenderPNG(t *testing.T) {
	if iconSize != 32 {
		t.Fatalf("图标尺寸应为 32，实际 %d", iconSize)
	}
	seen := make(map[string]iconState, 3)
	for _, state := range []iconState{iconNormal, iconPaused, iconIdle} {
		data := renderPNG(iconColors[state])
		if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("state %v 不是 PNG", state)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("state %v PNG 解码失败: %v", state, err)
		}
		if b := img.Bounds(); b.Dx() != iconSize || b.Dy() != iconSize {
			t.Fatalf("state %v 尺寸 = %v, want %dx%d", state, b, iconSize, iconSize)
		}
		// 板身内部：不透明的状态色。
		want := iconColors[state]
		r, g, b, a := img.At(16, 16).RGBA()
		if a != 0xffff || uint8(r>>8) != want.R || uint8(g>>8) != want.G || uint8(b>>8) != want.B {
			t.Fatalf("state %v 板身色 = %d,%d,%d,%d, want %v", state, r>>8, g>>8, b>>8, a>>8, want)
		}
		// 内容行：接近白色的实心行（预乘 alpha，取高字节比较）。
		r, g, b, a = img.At(16, 18).RGBA()
		if uint8(a>>8) == 0 || uint8(r>>8) != iconInk.R || uint8(g>>8) != iconInk.G || uint8(b>>8) != iconInk.B {
			t.Fatalf("state %v 内容行色 = %d,%d,%d,%d, want 预乘 %v", state, r>>8, g>>8, b>>8, a>>8, iconInk)
		}
		// 圆角之外：全透明。
		if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
			t.Fatalf("state %v 左上角应透明，alpha = %#x", state, a)
		}
		key := string(data)
		if prev, dup := seen[key]; dup {
			t.Fatalf("state %v 与 %v 的图标字节完全相同，三态应可区分", state, prev)
		}
		seen[key] = state
	}
}

func TestIconBytesUsesPlatformWrapper(t *testing.T) {
	for _, state := range []iconState{iconNormal, iconPaused, iconIdle} {
		data := iconBytes(state)
		if len(data) == 0 {
			t.Fatalf("state %v 图标为空", state)
		}
		if want := platformIcon(renderPNG(iconColors[state])); !bytes.Equal(data, want) {
			t.Fatalf("state %v 未按平台封装", state)
		}
		if again := iconBytes(state); string(again) != string(data) {
			t.Fatalf("state %v 图标缓存不稳定", state)
		}
	}
}

func TestWrapICO(t *testing.T) {
	payload := iconBytes(iconNormal)
	ico := wrapICO(payload)
	if len(ico) != 6+16+len(payload) {
		t.Fatalf("ICO 长度 = %d, want %d", len(ico), 6+16+len(payload))
	}
	le := binary.LittleEndian
	if got := le.Uint16(ico[0:]); got != 0 {
		t.Fatalf("reserved = %d, want 0", got)
	}
	if got := le.Uint16(ico[2:]); got != 1 {
		t.Fatalf("type = %d, want 1 (icon)", got)
	}
	if got := le.Uint16(ico[4:]); got != 1 {
		t.Fatalf("图像数 = %d, want 1", got)
	}
	if ico[6] != iconSize || ico[7] != iconSize {
		t.Fatalf("宽高字节 = %d,%d, want %d,%d", ico[6], ico[7], iconSize, iconSize)
	}
	if got := le.Uint16(ico[12:]); got != 32 {
		t.Fatalf("bitCount = %d, want 32", got)
	}
	if got := le.Uint32(ico[14:]); int(got) != len(payload) {
		t.Fatalf("数据长度 = %d, want %d", got, len(payload))
	}
	if got := le.Uint32(ico[18:]); int(got) != 22 {
		t.Fatalf("数据偏移 = %d, want 22", got)
	}
	if !bytes.Equal(ico[22:], payload) {
		t.Fatal("ICO 内嵌的 PNG 与原图不一致")
	}
}
