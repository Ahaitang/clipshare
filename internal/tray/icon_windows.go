//go:build windows

package tray

// platformIcon 在 Windows 上把 PNG 包进 ICO 外壳：systray 的托盘图标走
// LoadImage(IMAGE_ICON)，只认 ICO（ICO 内嵌 PNG 自 Vista 起受支持）。
func platformIcon(pngData []byte) []byte {
	return wrapICO(pngData)
}
