package tray

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"sync"

	"clipshare/internal/engine"
)

// 托盘图标在运行时用代码画出来，仓库里不放二进制资源。
const (
	iconSize  = 32 // 托盘图标边长（像素）
	iconScale = 4  // 先放大 4 倍绘制、再降采样，得到平滑边缘
)

// iconState 是三态图标。
type iconState int

const (
	iconNormal iconState = iota // 有 peer，同步中
	iconPaused                  // 已暂停
	iconIdle                    // 无可用节点
)

// iconStateFor 根据快照选图标：暂停优先于「无节点」。
func iconStateFor(snap engine.Snapshot) iconState {
	switch {
	case snap.Paused:
		return iconPaused
	case snap.Peers == 0:
		return iconIdle
	default:
		return iconNormal
	}
}

// iconColors 是三态配色：正常=绿、暂停=橙、零 peers=灰。
var iconColors = [3]color.RGBA{
	iconNormal: {R: 0x2f, G: 0xa8, B: 0x4f, A: 0xff},
	iconPaused: {R: 0xe8, G: 0x8b, B: 0x2e, A: 0xff},
	iconIdle:   {R: 0x8a, G: 0x90, B: 0x99, A: 0xff},
}

// iconInk 是内容行的白色（预乘 alpha，约 90% 不透明）。
var iconInk = color.RGBA{R: 0xe6, G: 0xe6, B: 0xe6, A: 0xe6}

var (
	iconMu    sync.Mutex
	iconCache = make(map[iconState][]byte, 3)
)

// iconBytes 返回某个状态的图标字节：非 Windows 是 PNG，Windows 是同一张图的
// ICO 封装（见 platformIcon）。
func iconBytes(state iconState) []byte {
	iconMu.Lock()
	defer iconMu.Unlock()
	if b, ok := iconCache[state]; ok {
		return b
	}
	b := platformIcon(renderPNG(iconColors[state]))
	iconCache[state] = b
	return b
}

// renderPNG 画一张 32x32 的剪贴板图标并编码成 PNG。
func renderPNG(c color.RGBA) []byte {
	const s = iconSize * iconScale
	hi := image.NewRGBA(image.Rect(0, 0, s, s))

	// 板身：圆角矩形。
	fillRoundRect(hi, 6*iconScale, 5*iconScale, 26*iconScale, 28*iconScale, 4*iconScale, c)
	// 顶部夹子：伸出板身之外的一小块。
	fillRoundRect(hi, 11*iconScale, 2*iconScale, 21*iconScale, 8*iconScale, 2*iconScale, c)
	// 三条白色内容行，小尺寸下也能认出是「剪贴板 / 列表」，而不是一块纯色方块。
	for _, y := range [3]int{12, 17, 22} {
		fillRoundRect(hi, 10*iconScale, y*iconScale, 22*iconScale, (y+2)*iconScale, iconScale, iconInk)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, downsample(hi, iconScale)); err != nil {
		log.Printf("tray: 编码图标失败: %v", err)
		return nil
	}
	return buf.Bytes()
}

// fillRoundRect 用颜色填充圆角矩形 [x0,x1) × [y0,y1)。
func fillRoundRect(img *image.RGBA, x0, y0, x1, y1, r int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if insideRoundRect(x, y, x0, y0, x1, y1, r) {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

// insideRoundRect 判断像素中心是否落在圆角矩形内。
func insideRoundRect(px, py, x0, y0, x1, y1, r int) bool {
	fx, fy := float64(px)+0.5, float64(py)+0.5
	lo, hiX, hiY := float64(r), float64(x1-r), float64(y1-r)
	// 把点夹到「直边区域」，只剩圆角需要比距离。
	cx := math.Min(math.Max(fx, float64(x0)+lo), hiX)
	cy := math.Min(math.Max(fy, float64(y0)+lo), hiY)
	dx, dy := fx-cx, fy-cy
	return dx*dx+dy*dy <= lo*lo
}

// downsample 用 box filter 做整数倍降采样，等价于抗锯齿。
func downsample(src *image.RGBA, factor int) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()/factor, b.Dy()/factor))
	n := uint32(factor * factor)
	for y := 0; y < dst.Bounds().Dy(); y++ {
		for x := 0; x < dst.Bounds().Dx(); x++ {
			var r, g, bl, a uint32
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					cr, cg, cb, ca := src.At(x*factor+dx, y*factor+dy).RGBA()
					r, g, bl, a = r+cr, g+cg, bl+cb, a+ca
				}
			}
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(r / n >> 8),
				G: uint8(g / n >> 8),
				B: uint8(bl / n >> 8),
				A: uint8(a / n >> 8),
			})
		}
	}
	return dst
}

// wrapICO 把一张 PNG 包成单图 ICO。
//
// Vista 之后的 Windows 支持 ICO 内嵌 PNG；而 systray 在 Windows 上用
// LoadImage(IMAGE_ICON) 从临时文件加载图标，只认 ICO 外壳。
func wrapICO(pngData []byte) []byte {
	const headerLen = 6 + 16
	out := make([]byte, headerLen+len(pngData))
	le := binary.LittleEndian
	le.PutUint16(out[0:], 0)                     // reserved
	le.PutUint16(out[2:], 1)                     // 1 = icon
	le.PutUint16(out[4:], 1)                     // 图像数
	out[6] = iconSize                            // 宽
	out[7] = iconSize                            // 高
	out[8] = 0                                   // 调色板颜色数，0 表示 ≥256 色
	out[9] = 0                                   // reserved
	le.PutUint16(out[10:], 1)                    // color planes
	le.PutUint16(out[12:], 32)                   // bits per pixel
	le.PutUint32(out[14:], uint32(len(pngData))) // 图像数据长度
	le.PutUint32(out[18:], headerLen)            // 图像数据偏移
	copy(out[headerLen:], pngData)
	return out
}
