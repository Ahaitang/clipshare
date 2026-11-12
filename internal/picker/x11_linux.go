//go:build linux

package picker

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"time"

	"github.com/BurntSushi/xgb"
	"github.com/BurntSushi/xgb/xproto"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// errX11Unavailable 表示自绘 X11 窗口在当前环境不可用（没有 DISPLAY、连不上 X、
// 或找不到可渲染中文的字体）。这是**预期内的降级信号**，不是故障：
// show() 收到它会退回 zenity。
var errX11Unavailable = errors.New("x11 自绘窗口不可用")

// 窗口几何与配色。弹出式窗口固定尺寸即可，不需要 resize。
const (
	pickW       = 640
	pickH       = 430
	pickTitleH  = 34
	pickSearchH = 32
	pickRowH    = 26
	pickFooterH = 26
	pickPad     = 14

	// pickFontSize 是窗口字号；72 DPI 下 14pt ≈ 14px，与系统面板观感接近。
	pickFontSize = 14.0
)

// revertToParent 对应 X11 协议的 RevertToParent（xgb 未导出这个枚举值）。
const revertToParent byte = 2

// 深色弹出层配色：与桌面主题无关，避免依赖 GTK/Qt 的设置。
var (
	pickBg       = color.RGBA{0x20, 0x21, 0x24, 0xff}
	pickTitleFg  = color.RGBA{0xf1, 0xf3, 0xf4, 0xff}
	pickFg       = color.RGBA{0xe8, 0xea, 0xed, 0xff}
	pickMuted    = color.RGBA{0x9a, 0xa0, 0xa6, 0xff}
	pickSelBg    = color.RGBA{0x3b, 0x82, 0xf6, 0xff}
	pickSelFg    = color.RGBA{0xff, 0xff, 0xff, 0xff}
	pickSep      = color.RGBA{0x3c, 0x40, 0x43, 0xff}
	pickSourceFg = color.RGBA{0x8a, 0xb4, 0xf8, 0xff}
)

// pickView 是渲染所需的全部状态（与 X11 无关，便于单测）。
type pickView struct {
	all    []Item
	shown  []Item // Filter 之后的结果
	query  string
	sel    int
	top    int // 列表滚动起点
	rows   int
	title  string
	filter func([]Item, string) []Item
}

// newPickView 构造初始状态。
func newPickView(items []Item, opts Options) *pickView {
	v := &pickView{
		all:    items,
		shown:  items,
		rows:   (pickH - pickTitleH - pickSearchH - pickFooterH) / pickRowH,
		title:  opts.Title,
		filter: Filter,
	}
	if v.title == "" {
		v.title = "clipshare 历史"
	}
	if v.rows < 1 {
		v.rows = 1
	}
	return v
}

// setQuery 更新搜索词并重置选中与滚动位置。
func (v *pickView) setQuery(q string) {
	v.query = q
	v.shown = v.filter(v.all, q)
	v.sel = 0
	v.top = 0
}

// move 移动选中项，并保证它留在可见范围内。
func (v *pickView) move(delta int) {
	v.sel = MoveIndex(v.sel, delta, len(v.shown))
	v.clampScroll()
}

// clampScroll 让选中项始终可见。
func (v *pickView) clampScroll() {
	if v.sel < v.top {
		v.top = v.sel
	}
	if v.sel >= v.top+v.rows {
		v.top = v.sel - v.rows + 1
	}
	if maxTop := len(v.shown) - v.rows; v.top > maxTop {
		v.top = maxTop
	}
	if v.top < 0 {
		v.top = 0
	}
}

// rowAt 把窗口内的 y 坐标映射成列表下标；不在列表区域时返回 -1。
func (v *pickView) rowAt(y int) int {
	if y < pickTitleH+pickSearchH || y >= pickH-pickFooterH {
		return -1
	}
	idx := v.top + (y-pickTitleH-pickSearchH)/pickRowH
	if idx < 0 || idx >= len(v.shown) {
		return -1
	}
	return idx
}

// renderPick 把当前状态画成一张位图。纯函数：不碰 X11，可在任意平台单测。
func renderPick(v *pickView, face font.Face) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, pickW, pickH))
	draw.Draw(img, img.Bounds(), &image.Uniform{pickBg}, image.Point{}, draw.Src)

	// 标题行
	fillRect(img, 0, 0, pickW, pickTitleH, color.RGBA{0x2b, 0x2d, 0x31, 0xff})
	drawText(img, face, pickPad, pickTitleH-11, v.title, pickTitleFg)

	// 搜索行
	drawText(img, face, pickPad, pickTitleH+pickSearchH-9, "过滤:", pickMuted)
	searchX := pickPad + textWidth(face, "过滤:") + 8
	if v.query == "" {
		drawText(img, face, searchX, pickTitleH+pickSearchH-9, "键入以过滤…", pickMuted)
	} else {
		drawText(img, face, searchX, pickTitleH+pickSearchH-9, v.query, pickFg)
	}
	fillRect(img, 0, pickTitleH+pickSearchH-1, pickW, 1, pickSep)

	// 列表
	listTop := pickTitleH + pickSearchH
	for i := 0; i < v.rows; i++ {
		idx := v.top + i
		if idx >= len(v.shown) {
			break
		}
		y := listTop + i*pickRowH
		it := v.shown[idx]
		fg := pickFg
		srcFg := pickSourceFg
		if idx == v.sel {
			fillRect(img, 4, y, pickW-8, pickRowH-2, pickSelBg)
			fg, srcFg = pickSelFg, pickSelFg
		}
		baseline := y + pickRowH - 8
		src := "[" + it.Source + "] "
		drawText(img, face, pickPad, baseline, src, srcFg)
		left := pickPad + textWidth(face, src)
		timeW := textWidth(face, it.When)
		avail := pickW - pickPad - timeW - 10 - left
		drawText(img, face, left, baseline, fitText(face, Preview(it.Content, 200), avail), fg)
		drawText(img, face, pickW-pickPad-timeW, baseline, it.When, pickMuted)
	}
	if len(v.shown) == 0 {
		drawText(img, face, pickPad, listTop+pickRowH-8, "没有匹配的历史条目", pickMuted)
	}

	// 底部统计与提示
	fillRect(img, 0, pickH-pickFooterH, pickW, 1, pickSep)
	pos := 0
	if len(v.shown) > 0 {
		pos = v.sel + 1
	}
	hint := fmt.Sprintf("共 %d 条 · 第 %d 条 · ↑↓ 选择   Enter 确定   Esc 取消", len(v.shown), pos)
	drawText(img, face, pickPad, pickH-9, hint, pickMuted)

	return img
}

// fillRect 用纯色填充矩形区域（自动裁剪到图像范围内）。
func fillRect(img *image.RGBA, x, y, w, h int, c color.Color) {
	r := image.Rect(x, y, x+w, y+h).Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	draw.Draw(img, r, &image.Uniform{c}, image.Point{}, draw.Src)
}

// drawText 在 (x, baseline) 处绘制文本。
func drawText(img *image.RGBA, face font.Face, x, baseline int, s string, c color.Color) {
	if s == "" {
		return
	}
	d := font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(x, baseline),
	}
	d.DrawString(s)
}

// textWidth 返回文本像素宽度。
func textWidth(face font.Face, s string) int {
	return font.MeasureString(face, s).Round()
}

// fitText 把文本裁剪到最多 maxPx 像素宽，超出部分用 … 收尾。
func fitText(face font.Face, s string, maxPx int) string {
	if maxPx <= 0 {
		return ""
	}
	if textWidth(face, s) <= maxPx {
		return s
	}
	runes := []rune(s)
	for i := len(runes) - 1; i > 0; i-- {
		cand := string(runes[:i]) + "…"
		if textWidth(face, cand) <= maxPx {
			return cand
		}
	}
	return "…"
}

// loadPickFace 按优先级加载一个能显示中文的字体。
//
// 失败时返回 errX11Unavailable 的包装：没有字体就无法自绘，交由 show() 降级到 zenity，
// 绝不画一片空白/乱码糊弄用户。
func loadPickFace(size float64) (font.Face, error) {
	candidates := []string{
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Medium.ttc",
		"/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",
		"/usr/share/fonts/truetype/arphic/uming.ttc",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var sf *sfnt.Font
		if bytes.HasPrefix(data, []byte("ttcf")) {
			coll, err := sfnt.ParseCollection(data)
			if err != nil {
				continue
			}
			// 集合里通常按地区排布（JP/KR/SC/TC），全部尝试直到有一个能解析出中文宽度。
			for i := 0; i < coll.NumFonts(); i++ {
				f, err := coll.Font(i)
				if err != nil {
					continue
				}
				if face := tryFace(f, size); face != nil {
					return face, nil
				}
			}
			continue
		}
		sf, err = sfnt.Parse(data)
		if err != nil {
			continue
		}
		if face := tryFace(sf, size); face != nil {
			return face, nil
		}
	}
	return nil, fmt.Errorf("%w: 未找到可用字体", errX11Unavailable)
}

// tryFace 尝试为字体创建 face，并确认它能渲染中文（避免选到纯拉丁字体导致方块）。
func tryFace(f *sfnt.Font, size float64) font.Face {
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	// 用一个常用汉字探测字形：Advance 为 0 说明该 face 没有这个字形。
	if adv, ok := face.GlyphAdvance('中'); !ok || adv <= 0 {
		return nil
	}
	return face
}

// showX11 创建一个 override-redirect 自绘窗口，独占键盘，返回用户选择。
//
// 为什么用 override-redirect：弹出层不应被窗口管理器接管（不要标题栏、不要出现在任务栏、
// 不改变焦点栈），这是桌面启动器类工具的通行做法。
func showX11(items []Item, opts Options) (Result, error) {
	face, err := loadPickFace(pickFontSize)
	if err != nil {
		return Result{}, err
	}

	conn, err := xgb.NewConn()
	if err != nil {
		return Result{}, fmt.Errorf("%w: 连接 X 失败: %v", errX11Unavailable, err)
	}
	defer conn.Close()

	setup := xproto.Setup(conn)
	screen := setup.DefaultScreen(conn)
	if screen == nil || screen.WidthInPixels == 0 {
		return Result{}, fmt.Errorf("%w: 无法获取屏幕信息", errX11Unavailable)
	}

	win, err := xproto.NewWindowId(conn)
	if err != nil {
		return Result{}, fmt.Errorf("%w: 分配窗口失败: %v", errX11Unavailable, err)
	}

	// 居中显示。
	x := int16((int(screen.WidthInPixels) - pickW) / 2)
	y := int16((int(screen.HeightInPixels) - pickH) / 3)
	if y < 0 {
		y = 0
	}

	const eventMask = xproto.EventMaskExposure | xproto.EventMaskKeyPress |
		xproto.EventMaskButtonPress | xproto.EventMaskButtonRelease |
		xproto.EventMaskStructureNotify

	if err := xproto.CreateWindowChecked(conn, 0, win, screen.Root, x, y, pickW, pickH, 0,
		xproto.WindowClassInputOutput, 0,
		xproto.CwBackPixel|xproto.CwOverrideRedirect|xproto.CwEventMask,
		[]uint32{0x202122, 1, eventMask},
	).Check(); err != nil {
		return Result{}, fmt.Errorf("%w: 创建窗口失败: %v", errX11Unavailable, err)
	}

	// 给窗口起个名字，便于 xwininfo 等工具识别（也是人工验收时的取证点）。
	setWindowTitle(conn, win, "clipshare 历史")

	gc, err := xproto.NewGcontextId(conn)
	if err != nil {
		return Result{}, fmt.Errorf("%w: 分配 GC 失败: %v", errX11Unavailable, err)
	}
	if err := xproto.CreateGCChecked(conn, gc, xproto.Drawable(win), 0, nil).Check(); err != nil {
		return Result{}, fmt.Errorf("%w: 创建 GC 失败: %v", errX11Unavailable, err)
	}

	if err := xproto.MapWindowChecked(conn, win).Check(); err != nil {
		return Result{}, fmt.Errorf("%w: 映射窗口失败: %v", errX11Unavailable, err)
	}
	// 必须先等窗口变成 viewable 再抓键盘：X 的 GrabKeyboard 要求窗口可映射，
	// 在 MapWindow 之后立刻调用会被拒（GrabNotViewable），结果是窗口开着却收不到按键——
	// 用户看到的按键会跑到别的窗口里去。这是我们实测踩过的坑，不是理论问题。
	if err := waitViewable(conn, win); err != nil {
		return Result{}, fmt.Errorf("%w: %v", errX11Unavailable, err)
	}
	_ = xproto.SetInputFocusChecked(conn, revertToParent, win, xproto.TimeCurrentTime).Check()
	// GrabKeyboard 在 xgb 里是「有回复」的请求，必须用 Reply() 取结果（Check() 会直接报错）。
	if _, err := xproto.GrabKeyboard(conn, false, win, xproto.TimeCurrentTime,
		xproto.GrabModeAsync, xproto.GrabModeAsync).Reply(); err != nil {
		// 抓不到键盘就不该留下一个「点不动的窗口」：直接降级到 zenity。
		return Result{}, fmt.Errorf("%w: 抢占键盘失败: %v", errX11Unavailable, err)
	}
	// 抓指针才能收到点击与滚轮；失败不致命（键盘仍可用）。
	_, _ = xproto.GrabPointer(conn, false, win, 0,
		xproto.GrabModeAsync, xproto.GrabModeAsync, xproto.AtomNone, xproto.AtomNone,
		xproto.TimeCurrentTime).Reply()

	defer func() {
		xproto.UngrabKeyboard(conn, xproto.TimeCurrentTime)
		xproto.UngrabPointer(conn, xproto.TimeCurrentTime)
		_ = xproto.DestroyWindowChecked(conn, win).Check()
		conn.Sync()
	}()

	view := newPickView(items, opts)
	keymap := newKeymap(conn, setup)

	// 渲染失败（例如服务器不支持所需像素格式）必须降级到 zenity，
	// 而不是留一个纯背景色的空白窗口让用户对着发呆。
	drawFrame := func() error {
		img := renderPick(view, face)
		if err := pushImage(conn, win, gc, img, setup, screen); err != nil {
			return err
		}
		conn.Sync()
		return nil
	}
	if err := drawFrame(); err != nil {
		return Result{}, fmt.Errorf("%w: %v", errX11Unavailable, err)
	}

	// 自检模式：画一帧后停留片刻即退出，供无交互环境下验证窗口真的画出来了。
	// 只由环境变量开启，正常使用不受影响。
	selfTest := os.Getenv("CLIPSHARE_PICK_SELFTEST") != ""
	deadline := time.Now().Add(3 * time.Second)

	for {
		var ev xgb.Event
		if selfTest {
			e, _ := conn.PollForEvent()
			ev = e
			if ev == nil {
				if time.Now().After(deadline) {
					return Result{OK: false}, nil
				}
				time.Sleep(20 * time.Millisecond)
				continue
			}
		} else {
			e, err := conn.WaitForEvent()
			if err != nil || e == nil {
				return Result{OK: false}, nil
			}
			ev = e
		}

		switch e := ev.(type) {
		case xproto.ExposeEvent:
			_ = drawFrame()
		case xproto.KeyPressEvent:
			key := keymap.resolve(e.Detail, e.State)
			switch key {
			case keyEsc:
				return Result{OK: false}, nil
			case keyEnter:
				if len(view.shown) == 0 {
					continue
				}
				return Result{ID: view.shown[view.sel].ID, OK: true}, nil
			case keyUp:
				view.move(-1)
			case keyDown:
				view.move(1)
			case keyPgUp:
				view.move(-view.rows)
			case keyPgDn:
				view.move(view.rows)
			case keyHome:
				view.sel = 0
				view.clampScroll()
			case keyEnd:
				view.sel = len(view.shown) - 1
				view.clampScroll()
			case keyBackspace:
				if r := []rune(view.query); len(r) > 0 {
					view.setQuery(string(r[:len(r)-1]))
				}
			default:
				if r := key.rune(); r != 0 {
					view.setQuery(view.query + string(r))
				}
			}
			_ = drawFrame()
		case xproto.ButtonPressEvent:
			yy := int(e.EventY)
			if e.Detail == 4 { // 滚轮上
				view.move(-1)
				_ = drawFrame()
				continue
			}
			if e.Detail == 5 { // 滚轮下
				view.move(1)
				_ = drawFrame()
				continue
			}
			if idx := view.rowAt(yy); idx >= 0 {
				return Result{ID: view.shown[idx].ID, OK: true}, nil
			}
		}
	}
}

// maxPutImageBytes 是单次 PutImage 请求的保守上限。
//
// X11 的经典请求上限是 262140 字节（4 × 65535）；虽然支持 BIG-REQ 的服务器允许更大，
// 但并非所有服务器都启用。640×430 的整帧是 1.1MB，一次推过去会被服务器以 BadLength
// 丢弃、窗口只剩背景色——所以必须按行分带推送。
const maxPutImageBytes = 256 * 1024

// waitViewable 等待窗口进入 viewable 状态（MapWindow 是异步的）。
//
// 用 GetWindowAttributes 轮询而不是等 MapNotify：本窗口的事件掩码里没有 StructureNotify
// 的竞争问题，轮询更简单且不会被其它事件打乱。最多等 500ms。
func waitViewable(conn *xgb.Conn, win xproto.Window) error {
	for i := 0; i < 50; i++ {
		attrs, err := xproto.GetWindowAttributes(conn, win).Reply()
		if err == nil && attrs.MapState == xproto.MapStateViewable {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("窗口未在 500ms 内变为可映射状态")
}

// pushImage 把位图按行分带推送到窗口。
func pushImage(conn *xgb.Conn, win xproto.Window, gc xproto.Gcontext, img *image.RGBA, setup *xproto.SetupInfo, screen *xproto.ScreenInfo) error {
	masks := visualMasks(setup, screen)
	littleEndian := setup.ImageByteOrder == xproto.ImageOrderLSBFirst

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	rowsPer := maxPutImageBytes / (w * 4)
	if rowsPer < 1 {
		rowsPer = 1
	}
	for y0 := 0; y0 < h; y0 += rowsPer {
		y1 := y0 + rowsPer
		if y1 > h {
			y1 = h
		}
		band, ok := img.SubImage(image.Rect(b.Min.X, b.Min.Y+y0, b.Min.X+w, b.Min.Y+y1)).(*image.RGBA)
		if !ok {
			return fmt.Errorf("子图类型不符")
		}
		data := encodeXImage(band, masks, littleEndian)
		if err := xproto.PutImageChecked(conn, xproto.ImageFormatZPixmap, xproto.Drawable(win), gc,
			uint16(w), uint16(y1-y0), 0, int16(y0), 0, screen.RootDepth, data).Check(); err != nil {
			return fmt.Errorf("PutImage(行 %d-%d): %w", y0, y1, err)
		}
	}
	return nil
}

// rgbMasks 是从 visual 里取出的通道位掩码。
type rgbMasks struct{ r, g, b uint32 }

// visualMasks 查询根窗口 visual 的 RGB 掩码；查不到时退回 TrueColor 24 位的标准掩码。
//
// 必须按实际 visual 计算：硬编码在 30 位色深或 BGR 排列的服务器上会直接花屏。
func visualMasks(setup *xproto.SetupInfo, screen *xproto.ScreenInfo) rgbMasks {
	fallback := rgbMasks{0xff0000, 0xff00, 0xff}
	for _, d := range screen.AllowedDepths {
		if d.Depth != screen.RootDepth {
			continue
		}
		for _, v := range d.Visuals {
			if v.VisualId == screen.RootVisual {
				if v.Class == xproto.VisualClassTrueColor || v.Class == xproto.VisualClassDirectColor {
					if v.RedMask != 0 && v.GreenMask != 0 && v.BlueMask != 0 {
						return rgbMasks{v.RedMask, v.GreenMask, v.BlueMask}
					}
				}
			}
		}
	}
	_ = setup
	return fallback
}

// encodeXImage 把 image.RGBA 编码成 X 服务器期望的 ZPixmap 字节流。
//
// 24/32 位深度下每个像素占 4 字节，字节序由服务器的位图字节序决定（x86 是 LSBFirst）。
func encodeXImage(img *image.RGBA, m rgbMasks, littleEndian bool) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]byte, w*h*4)
	rs, gs, bs := shiftOf(m.r), shiftOf(m.g), shiftOf(m.b)
	i := 0
	for y := 0; y < h; y++ {
		row := img.Pix[y*img.Stride:]
		for x := 0; x < w; x++ {
			p := row[x*4:]
			px := uint32(p[0])<<rs | uint32(p[1])<<gs | uint32(p[2])<<bs
			if littleEndian {
				out[i], out[i+1], out[i+2], out[i+3] = byte(px), byte(px>>8), byte(px>>16), byte(px>>24)
			} else {
				out[i], out[i+1], out[i+2], out[i+3] = byte(px>>24), byte(px>>16), byte(px>>8), byte(px)
			}
			i += 4
		}
	}
	return out
}

// shiftOf 返回掩码最低置位的位置（0xff0000 → 16）。
func shiftOf(mask uint32) uint {
	if mask == 0 {
		return 0
	}
	s := uint(0)
	for mask&1 == 0 {
		mask >>= 1
		s++
	}
	return s
}

// setWindowTitle 设置窗口名（UTF-8），失败不影响功能。
func setWindowTitle(conn *xgb.Conn, win xproto.Window, title string) {
	netWMName, err := xproto.InternAtom(conn, false, uint16(len("_NET_WM_NAME")), "_NET_WM_NAME").Reply()
	if err != nil {
		return
	}
	utf8Atom, err := xproto.InternAtom(conn, false, uint16(len("UTF8_STRING")), "UTF8_STRING").Reply()
	if err != nil {
		return
	}
	_ = xproto.ChangeProperty(conn, xproto.PropModeReplace, win, netWMName.Atom,
		utf8Atom.Atom, 8, uint32(len(title)), []byte(title))
}

// 特殊键的键码（keysym）
const (
	keyEsc       = 0xff1b
	keyEnter     = 0xff0d
	keyUp        = 0xff52
	keyDown      = 0xff54
	keyPgUp      = 0xff55
	keyPgDn      = 0xff56
	keyHome      = 0xff50
	keyEnd       = 0xff57
	keyBackspace = 0xff08
)

// key 是解析后的按键：要么是特殊键常量，要么是可打印字符。
type key uint32

// rune 返回可打印字符；非可打印键返回 0。
func (k key) rune() rune {
	if k >= 0x20 && k <= 0x7e {
		return rune(k)
	}
	return 0
}

// keymap 保存键盘映射表，用于把 keycode+state 解析成 keysym。
type keymap struct {
	minKeycode xproto.Keycode
	perKeycode byte
	keysyms    []xproto.Keysym
}

// newKeymap 一次性取出全部键映射（弹出层生命周期内不会变）。
func newKeymap(conn *xgb.Conn, setup *xproto.SetupInfo) *keymap {
	min := setup.MinKeycode
	count := byte(setup.MaxKeycode - setup.MinKeycode + 1)
	reply, err := xproto.GetKeyboardMapping(conn, min, count).Reply()
	if err != nil {
		return &keymap{minKeycode: min, perKeycode: 1}
	}
	return &keymap{
		minKeycode: min,
		perKeycode: reply.KeysymsPerKeycode,
		keysyms:    reply.Keysyms,
	}
}

// resolve 把 keycode 与修饰位解析成 keysym。只区分 Shift：数字/符号需要它是必需的，
// 而 Ctrl/Alt 在这里不参与字符输入（本窗口不提供快捷键组合）。
func (k *keymap) resolve(keycode xproto.Keycode, state uint16) key {
	if k == nil || k.perKeycode == 0 || len(k.keysyms) == 0 {
		return 0
	}
	idx := int(keycode-k.minKeycode) * int(k.perKeycode)
	if idx < 0 || idx >= len(k.keysyms) {
		return 0
	}
	pos := idx
	if state&xproto.KeyButMaskShift != 0 {
		if idx+1 < len(k.keysyms) {
			pos = idx + 1
		}
	}
	return key(k.keysyms[pos])
}
