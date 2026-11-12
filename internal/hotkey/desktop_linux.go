//go:build linux

package hotkey

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNoDesktopIntegration 表示当前桌面环境不支持「由程序自动注册快捷键」。
//
// Linux 没有 Win32 RegisterHotKey 那样的进程内全局热键接口，
// 主流做法是让桌面环境（GNOME/KDE/...）把组合键绑到一个命令上。
// 我们只实现 GNOME（gsettings）这一条：其他桌面返回本错误，
// 由调用方提示用户手动绑定，而不是静默失败。
var ErrNoDesktopIntegration = errors.New("当前桌面环境不支持自动注册快捷键")

// 本程序在 GNOME 自定义快捷键里使用的槽位名。
const gnomeSlotName = "clipshare"

const (
	mediaKeysSchema  = "org.gnome.settings-daemon.plugins.media-keys"
	keybindingSchema = "org.gnome.settings-daemon.plugins.media-keys.custom-keybinding"
	slotsKey         = "custom-keybindings"
	slotBasePath     = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/"
	targetBinding    = "<Control><Alt>v"
)

// EnsureDesktopShortcut 让 GNOME 把 Ctrl+Alt+V 绑定到 `<exePath> pick`。
//
// 返回一段人类可读的状态说明（用于日志），error 只在「无法完成且需要用户介入」时返回。
//
// 行为约定（保守，绝不抢别人的键）：
//   - 已经存在属于本程序的槽位：只修正命令路径（例如从旧的手工拷贝路径改到 /usr/bin），保留用户改过的其它字段；
//   - 不存在、且 Ctrl+Alt+V 没被别的程序占用：新建一个槽位；
//   - 组合键已被别的程序占用：报错，让用户自己决定，不静默覆盖。
func EnsureDesktopShortcut(exePath string) (string, error) {
	if _, err := exec.LookPath("gsettings"); err != nil {
		return "", fmt.Errorf("%w: 找不到 gsettings", ErrNoDesktopIntegration)
	}
	if !isGNOME() {
		return "", fmt.Errorf("%w: 当前不是 GNOME 会话", ErrNoDesktopIntegration)
	}

	command := strings.TrimSpace(exePath) + " pick"
	slots, err := readStringList(mediaKeysSchema, slotsKey)
	if err != nil {
		return "", fmt.Errorf("读取 GNOME 快捷键列表失败: %w", err)
	}

	// 1) 已有属于本程序的槽位？
	for _, slot := range slots {
		cmdVal, _ := gsettingsGet(keybindingSchema+":"+slot, "command")
		if !isOurCommand(cmdVal) {
			continue
		}
		if strings.TrimSpace(cmdVal) != command {
			if err := gsettingsSet(keybindingSchema+":"+slot, "command", command); err != nil {
				return "", fmt.Errorf("更新快捷键命令失败: %w", err)
			}
			return fmt.Sprintf("已把快捷键命令更新为 %q（绑定不变）", command), nil
		}
		return fmt.Sprintf("快捷键已就绪：%s → %s", targetBinding, command), nil
	}

	// 2) Ctrl+Alt+V 是否被别的程序占用
	for _, slot := range slots {
		binding, _ := gsettingsGet(keybindingSchema+":"+slot, "binding")
		if normalizeAccel(binding) == normalizeAccel(targetBinding) {
			name, _ := gsettingsGet(keybindingSchema+":"+slot, "name")
			return "", fmt.Errorf("组合键 %s 已被「%s」占用，未改动；请手动改绑或在设置里调整", targetBinding, strings.Trim(name, "'"))
		}
	}

	// 3) 新建槽位并追加到列表（追加而非覆盖，绝不弄丢别人的快捷键）
	slot := slotBasePath + gnomeSlotName + "/"
	if err := gsettingsSet(keybindingSchema+":"+slot, "name", "clipshare 历史"); err != nil {
		return "", fmt.Errorf("写入快捷键名称失败: %w", err)
	}
	if err := gsettingsSet(keybindingSchema+":"+slot, "command", command); err != nil {
		return "", fmt.Errorf("写入快捷键命令失败: %w", err)
	}
	if err := gsettingsSet(keybindingSchema+":"+slot, "binding", targetBinding); err != nil {
		return "", fmt.Errorf("写入快捷键组合失败: %w", err)
	}
	slots = append(slots, slot)
	if err := writeStringList(mediaKeysSchema, slotsKey, slots); err != nil {
		return "", fmt.Errorf("登记快捷键槽位失败: %w", err)
	}
	return fmt.Sprintf("已自动注册快捷键：%s → %s", targetBinding, command), nil
}

// isOurCommand 判断某个槽位的命令是不是本程序的 pick 子命令。
func isOurCommand(cmd string) bool {
	cmd = strings.Trim(strings.TrimSpace(cmd), `'"`)
	if !strings.HasSuffix(cmd, "pick") {
		return false
	}
	return strings.Contains(cmd, "clipshare")
}

// normalizeAccel 归一化加速键写法，便于比较（大小写、空格不敏感）。
func normalizeAccel(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(strings.Trim(s, "'")), " ", ""))
}

// isGNOME 通过环境变量判断当前桌面；非 GNOME 不做任何改动。
func isGNOME() bool {
	out, err := exec.Command("sh", "-c", `printf '%s' "$XDG_CURRENT_DESKTOP"`).Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToUpper(string(out)), "GNOME") ||
		strings.Contains(strings.ToUpper(string(out)), "UNITY") ||
		strings.Contains(strings.ToUpper(string(out)), "UBUNTU")
}

// readStringList 读取形如 ['a', 'b'] 的 gsettings 字符串数组。
func readStringList(schema, key string) ([]string, error) {
	out, err := exec.Command("gsettings", "get", schema, key).Output()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	raw = strings.TrimPrefix(raw, "@as")
	raw = strings.Trim(strings.TrimSpace(raw), "[]")
	if raw == "" {
		return nil, nil
	}
	var slots []string
	for _, part := range strings.Split(raw, ",") {
		if v := strings.Trim(strings.TrimSpace(part), "'\""); v != "" {
			slots = append(slots, v)
		}
	}
	return slots, nil
}

// writeStringList 写入字符串数组（gsettings 需要 GVariant 的方括号+引号格式）。
func writeStringList(schema, key string, values []string) error {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, "'"+v+"'")
	}
	return exec.Command("gsettings", "set", schema, key, "["+strings.Join(quoted, ", ")+"]").Run()
}

func gsettingsGet(schema, key string) (string, error) {
	out, err := exec.Command("gsettings", "get", schema, key).Output()
	return strings.TrimSpace(string(out)), err
}

func gsettingsSet(schema, key, value string) error {
	return exec.Command("gsettings", "set", schema, key, value).Run()
}
