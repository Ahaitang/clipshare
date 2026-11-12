//go:build linux

package picker

import (
	"image"
	"image/color"
	"strings"
	"testing"

	"golang.org/x/image/font"
)

// mustLoadFace 加载真实字体；本机没有可用字体时跳过（CI 可能没有 CJK 字体）。
func mustLoadFace(t *testing.T) font.Face {
	t.Helper()
	f, err := loadPickFace(pickFontSize)
	if err != nil {
		t.Skipf("本机没有可用字体，跳过渲染断言: %v", err)
	}
	return f
}

// ink 统计矩形区域内与背景色不同的像素数量。
func ink(img *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c != pickBg {
				n++
			}
		}
	}
	return n
}

func TestRenderPickDimensions(t *testing.T) {
	face := mustLoadFace(t)
	defer func() { _ = face.Close() }()
	v := newPickView(FromEntries(entries(), "dev-self"), Options{})
	img := renderPick(v, face)
	if got := img.Bounds(); got.Dx() != pickW || got.Dy() != pickH {
		t.Fatalf("尺寸 = %v, want %dx%d", got, pickW, pickH)
	}
}

func TestRenderPickDrawsTitleListFooter(t *testing.T) {
	face := mustLoadFace(t)
	v := newPickView(FromEntries(entries(), "dev-self"), Options{})
	img := renderPick(v, face)

	titleBand := image.Rect(0, 0, pickW, pickTitleH)
	listBand := image.Rect(0, pickTitleH+pickSearchH, pickW, pickTitleH+pickSearchH+pickRowH*2)
	footerBand := image.Rect(0, pickH-pickFooterH, pickW, pickH)

	for name, r := range map[string]image.Rectangle{"标题": titleBand, "列表": listBand, "页脚": footerBand} {
		if ink(img, r) == 0 {
			t.Errorf("%s区域没有任何文字像素（渲染可能整片空白）", name)
		}
	}
}

func TestRenderPickRendersCJK(t *testing.T) {
	face := mustLoadFace(t)
	// 用纯中文内容：若字体选错成纯拉丁字体，这里会几乎没有字形像素。
	v := newPickView([]Item{{ID: "a", Source: "本机", Content: "简体中文测试内容", When: "11-12 10:00"}}, Options{})
	img := renderPick(v, face)
	r := image.Rect(pickPad, pickTitleH+pickSearchH, pickW/2, pickTitleH+pickSearchH+pickRowH)
	if n := ink(img, r); n < 20 {
		t.Fatalf("中文行墨迹过少（%d 像素），字体可能不含 CJK 字形", n)
	}
}

func TestRenderPickHighlightsOnlySelectedRow(t *testing.T) {
	face := mustLoadFace(t)
	v := newPickView(FromEntries(entries(), "dev-self"), Options{})
	v.sel = 1
	img := renderPick(v, face)

	listTop := pickTitleH + pickSearchH
	rowRect := func(i int) image.Rectangle {
		return image.Rect(4, listTop+i*pickRowH, pickW-4, listTop+i*pickRowH+pickRowH-2)
	}
	countSel := func(r image.Rectangle) int {
		n := 0
		for y := r.Min.Y; y < r.Max.Y; y++ {
			for x := r.Min.X; x < r.Max.X; x++ {
				if img.RGBAAt(x, y) == pickSelBg {
					n++
				}
			}
		}
		return n
	}
	if got := countSel(rowRect(1)); got < rowRect(1).Dx()*rowRect(1).Dy()/2 {
		t.Errorf("选中行高亮面积不足: %d", got)
	}
	if got := countSel(rowRect(0)); got != 0 {
		t.Errorf("未选中的行不应有高亮像素，实际 %d", got)
	}
}

func TestRenderPickEmptyListShowsHint(t *testing.T) {
	face := mustLoadFace(t)
	v := newPickView(nil, Options{})
	img := renderPick(v, face) // 不得 panic
	listBand := image.Rect(0, pickTitleH+pickSearchH, pickW, pickH-pickFooterH)
	if ink(img, listBand) == 0 {
		t.Error("空列表时应显示「没有匹配的历史条目」提示")
	}
}

func TestRenderPickLongContentStaysInBounds(t *testing.T) {
	face := mustLoadFace(t)
	long := strings.Repeat("很长的内容片段", 200)
	v := newPickView([]Item{{ID: "a", Source: "本机", Content: long, When: "11-12 10:00"}}, Options{})
	img := renderPick(v, face)
	// 行内文字不得越过右边界（越过说明截断逻辑失效）
	for y := pickTitleH + pickSearchH; y < pickTitleH+pickSearchH+pickRowH; y++ {
		for x := pickW - 1; x >= 0; x-- {
			if img.RGBAAt(x, y) != pickBg && img.RGBAAt(x, y) != pickSelBg {
				if x >= pickW-2 {
					t.Fatalf("y=%d 时文字贴到右边界 x=%d，可能溢出", y, x)
				}
				break
			}
		}
	}
}

func TestViewRowAtAndScroll(t *testing.T) {
	v := newPickView(make([]Item, 100), Options{})
	if v.rows < 1 {
		t.Fatalf("rows = %d", v.rows)
	}
	// 列表区域顶部第一行
	if got := v.rowAt(pickTitleH + pickSearchH + 1); got != 0 {
		t.Errorf("首行下标 = %d, want 0", got)
	}
	// 标题/页脚区域不命中
	if got := v.rowAt(2); got != -1 {
		t.Errorf("标题区应返回 -1，实际 %d", got)
	}
	if got := v.rowAt(pickH - 2); got != -1 {
		t.Errorf("页脚区应返回 -1，实际 %d", got)
	}
	// 向下移动超出可见范围时必须滚动
	v.sel = 0
	v.move(v.rows + 3)
	if v.top == 0 {
		t.Errorf("选中项超出可见范围后应滚动，top 仍为 0")
	}
	if v.sel < v.top || v.sel >= v.top+v.rows {
		t.Errorf("选中项不可见: sel=%d top=%d rows=%d", v.sel, v.top, v.rows)
	}
}

func TestViewSetQueryFiltersAndResets(t *testing.T) {
	v := newPickView(FromEntries(entries(), "dev-self"), Options{})
	v.sel = 2
	v.setQuery("hello")
	if v.sel != 0 || v.top != 0 {
		t.Errorf("过滤后应重置选中与滚动: sel=%d top=%d", v.sel, v.top)
	}
	if len(v.shown) != 1 || v.shown[0].ID != "id2" {
		t.Errorf("过滤结果不对: %+v", v.shown)
	}
	v.setQuery("")
	if len(v.shown) != 3 {
		t.Errorf("清空查询应恢复全部条目，实际 %d", len(v.shown))
	}
}

func TestEncodeXImageMasks(t *testing.T) {
	// 2×1：纯红、纯蓝；标准 TrueColor 掩码下应得到 0x00ff0000 / 0x000000ff。
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.SetRGBA(0, 0, color.RGBA{255, 0, 0, 255})
	img.SetRGBA(1, 0, color.RGBA{0, 0, 255, 255})
	masks := rgbMasks{0xff0000, 0xff00, 0xff}

	le := encodeXImage(img, masks, true)
	if le[0] != 0x00 || le[1] != 0x00 || le[2] != 0xff {
		t.Errorf("红色像素 LSBFirst 编码错误: % x", le[:4])
	}
	if le[4] != 0xff || le[5] != 0x00 || le[6] != 0x00 {
		t.Errorf("蓝色像素 LSBFirst 编码错误: % x", le[4:8])
	}
	be := encodeXImage(img, masks, false)
	if be[1] != 0xff || be[2] != 0x00 {
		t.Errorf("红色像素 MSBFirst 编码错误: % x", be[:4])
	}
	if len(le) != 8 || len(be) != 8 {
		t.Errorf("字节数错误: le=%d be=%d", len(le), len(be))
	}
}

func TestShiftOf(t *testing.T) {
	cases := map[uint32]uint{0: 0, 0xff: 0, 0xff00: 8, 0xff0000: 16}
	for in, want := range cases {
		if got := shiftOf(in); got != want {
			t.Errorf("shiftOf(%#x) = %d, want %d", in, got, want)
		}
	}
}

func TestFitText(t *testing.T) {
	face := mustLoadFace(t)
	if got := fitText(face, "短", 500); got != "短" {
		t.Errorf("放得下时不应截断: %q", got)
	}
	long := strings.Repeat("很长的内容", 50)
	got := fitText(face, long, 100)
	if len([]rune(got)) >= len([]rune(long)) {
		t.Errorf("超宽文本未被截断: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("截断应以 … 结尾: %q", got)
	}
}
