package picker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// 位置持久化格式：单行 "x,y"。
//
// 为什么用文件而不是配置项：选择窗口的位置是纯 UI 偏好，写进 config.json 会让
// 用户手改配置时多一份噪音；独立小文件坏了也不影响同步（解析失败就回落到居中）。

// LoadPos 读取上次记住的窗口左上角坐标。文件不存在或内容非法时 ok=false。
func LoadPos(path string) (x, y int, ok bool) {
	if path == "" {
		return 0, 0, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ",")
	if len(parts) != 2 {
		return 0, 0, false
	}
	px, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	py, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return px, py, true
}

// SavePos 记住窗口左上角坐标。失败只返回错误，调用方可忽略（不影响使用）。
func SavePos(path string, x, y int) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建位置文件目录失败: %w", err)
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d,%d\n", x, y)), 0o600)
}

// ClampPos 把窗口左上角夹到「窗口完整落在屏幕内」的合法范围。
//
// 必须做这个夹紧：用户可能把窗口拖到副屏，之后副屏拔掉、或者分辨率变了，
// 记住的位置就会把窗口放到看不见的地方——那种「按了快捷键什么都没发生」最难排查。
func ClampPos(x, y, screenW, screenH, winW, winH int) (int, int) {
	if screenW <= 0 || screenH <= 0 {
		return x, y
	}
	maxX := screenW - winW
	maxY := screenH - winH
	if maxX < 0 {
		maxX = 0
	}
	if maxY < 0 {
		maxY = 0
	}
	if x > maxX {
		x = maxX
	}
	if y > maxY {
		y = maxY
	}
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	return x, y
}
