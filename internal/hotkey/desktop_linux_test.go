//go:build linux

package hotkey

import "testing"

func TestUnquote(t *testing.T) {
	cases := map[string]string{
		"'/usr/bin/clipshare pick'": "/usr/bin/clipshare pick",
		`"/usr/bin/clipshare pick"`: "/usr/bin/clipshare pick",
		"  'abc'  ":                 "abc",
		"plain":                     "plain",
		"'":                         "'", // 只有半个引号时原样返回
		"''":                        "",
	}
	for in, want := range cases {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsOurCommand(t *testing.T) {
	yes := []string{
		"/usr/bin/clipshare pick",
		"'/home/u/.local/bin/clipshare pick'",
		`"/opt/clipshare/clipshare pick"`,
		"  /usr/bin/clipshare pick  ",
	}
	for _, c := range yes {
		if !isOurCommand(c) {
			t.Errorf("%q 应被识别为本程序的命令", c)
		}
	}
	no := []string{
		"",
		"flameshot gui",
		"/usr/bin/clipshare",        // 没有 pick 子命令
		"/usr/bin/clipshare picker", // 后缀不是 pick
		"/usr/bin/othertool pick",   // 不是本程序
		"~/.local/bin/win11-clipboard-history.AppImage",
	}
	for _, c := range no {
		if isOurCommand(c) {
			t.Errorf("%q 不应被识别为本程序的命令", c)
		}
	}
}

func TestNormalizeAccel(t *testing.T) {
	same := [][2]string{
		{"<Control><Alt>v", "<control><alt>V"},
		{"'<Control><Alt>v'", "<Control><Alt>v"},
		{" <Super>v ", "<Super>v"},
	}
	for _, p := range same {
		if normalizeAccel(p[0]) != normalizeAccel(p[1]) {
			t.Errorf("%q 与 %q 应视为同一组合键", p[0], p[1])
		}
	}
	if normalizeAccel("<Control><Alt>v") == normalizeAccel("<Control><Alt>w") {
		t.Error("不同的键不应相等")
	}
}
