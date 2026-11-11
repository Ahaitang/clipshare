//go:build !windows

package tray

// platformIcon 在 Linux/macOS 上直接给 PNG：systray 用 image/png 解码后
// 转成 ARGB 像素交给 StatusNotifierItem。
func platformIcon(pngData []byte) []byte {
	return pngData
}
