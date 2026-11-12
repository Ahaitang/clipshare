//go:build linux

package picker

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// zenityCancelExitCode 是 zenity 在用户取消时的退出码（GTK 对话框约定）。
const zenityCancelExitCode = 1

// showZenity 用 GNOME 自带的 zenity 列表对话框做选择。
//
// 列布局：1=ID（隐藏，只用来回传选中项）2=内容 3=来源 4=时间。
// ID 放在第 1 列并用 --hide-column=1 隐藏、--print-column=1 只打印它，
// 这样不必从展示文本里反解条目（内容可能重复、可能含分隔符）。
//
// 键入搜索交给 GTK（--mid-search 改成子串匹配），比自己做输入处理更贴桌面习惯；
// 因此这条路径不使用 Filter。
func showZenity(path string, items []Item, opts Options) (Result, error) {
	title := opts.Title
	if title == "" {
		title = "clipshare 历史（↑↓ 选择，输入即过滤，回车确认，Esc 取消）"
	}

	args := []string{
		"--list",
		"--title=" + title,
		"--width=780", "--height=440",
		"--hide-header",
		"--hide-column=1",
		"--print-column=1",
		"--mid-search",
		"--column=", // 隐藏列：条目 ID
		"--column=内容",
		"--column=来源",
		"--column=时间",
	}
	for _, it := range items {
		args = append(args, it.ID, Preview(it.Content, 120), it.Source, it.When)
	}

	var out, stderr bytes.Buffer
	cmd := exec.Command(path, args...)
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == zenityCancelExitCode {
			return Result{OK: false}, nil
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return Result{}, fmt.Errorf("zenity 失败: %v (%s)", err, msg)
		}
		return Result{}, fmt.Errorf("zenity 失败: %w", err)
	}

	id := strings.TrimSpace(out.String())
	if id == "" {
		return Result{OK: false}, nil
	}
	return Result{ID: id, OK: true}, nil
}
