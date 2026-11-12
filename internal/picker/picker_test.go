package picker

import (
	"errors"
	"strings"
	"testing"
	"time"

	"clipshare/internal/model"
)

func entries() []model.Entry {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.Local)
	return []model.Entry{
		{ID: "id1", Origin: "dev-self", OriginName: "haitang", Content: "第一行\n第二行", RecvAt: now},
		{ID: "id2", Origin: "dev-other", OriginName: "DESKTOP-UIRQT1R", Content: "Hello World", RecvAt: now.Add(-time.Minute)},
		{ID: "id3", Origin: "dev-self", OriginName: "haitang", Content: "ERROR: disk full", RecvAt: now.Add(-2 * time.Minute)},
	}
}

func TestFromEntriesSourceLabel(t *testing.T) {
	items := FromEntries(entries(), "dev-self")
	if len(items) != 3 {
		t.Fatalf("条数 = %d, want 3", len(items))
	}
	if items[0].Source != "本机" || items[2].Source != "本机" {
		t.Errorf("本机条目来源标记错误: %q / %q", items[0].Source, items[2].Source)
	}
	if items[1].Source != "DESKTOP-UIRQT1R" {
		t.Errorf("远端条目应显示节点名，实际 %q", items[1].Source)
	}
	if items[1].ID != "id2" || items[1].When == "" {
		t.Errorf("ID/时间未填充: %+v", items[1])
	}
	// 顺序必须沿用传入顺序（历史已是最新在前），否则用户键入过滤时会看到列表乱跳
	if items[0].ID != "id1" || items[2].ID != "id3" {
		t.Errorf("顺序被改动了: %+v", items)
	}
}

func TestFilter(t *testing.T) {
	items := FromEntries(entries(), "dev-self")
	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"id1", "id2", "id3"}},
		{"hello", []string{"id2"}},     // 大小写不敏感
		{"HELLO", []string{"id2"}},     // 大写也能命中
		{"ERROR", []string{"id3"}},     // 匹配内容
		{"desktop", []string{"id2"}},   // 匹配来源名
		{"第二行", []string{"id1"}},       // 中文按子串匹配
		{"不存在的词", nil},                 // 无匹配
		{"  hello  ", []string{"id2"}}, // 查询词两侧空白被忽略
	}
	for _, c := range cases {
		got := Filter(items, c.query)
		var ids []string
		for _, it := range got {
			ids = append(ids, it.ID)
		}
		if strings.Join(ids, ",") != strings.Join(c.want, ",") {
			t.Errorf("Filter(%q) = %v, want %v", c.query, ids, c.want)
		}
	}
}

func TestPreview(t *testing.T) {
	cases := []struct {
		in    string
		width int
		want  string
	}{
		{"单行", 10, "单行"},
		{"第一行\n第二行", 20, "第一行 ⏎ 第二行"},
		{"a\r\nb", 20, "a ⏎ b"},
		{"a\tb", 20, "a  b"},
		{strings.Repeat("中", 10), 4, "中中中中…"},
	}
	for _, c := range cases {
		if got := Preview(c.in, c.width); got != c.want {
			t.Errorf("Preview(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestMoveIndex(t *testing.T) {
	cases := []struct {
		cur, delta, n, want int
	}{
		{0, 1, 3, 1},
		{2, 1, 3, 2},  // 下边界夹紧
		{0, -1, 3, 0}, // 上边界夹紧
		{5, 1, 3, 2},  // 越界修正
		{0, 1, 0, 0},  // 空列表
		{-1, 1, 3, 0}, // 负起点
	}
	for _, c := range cases {
		if got := MoveIndex(c.cur, c.delta, c.n); got != c.want {
			t.Errorf("MoveIndex(%d,%d,%d) = %d, want %d", c.cur, c.delta, c.n, got, c.want)
		}
	}
}

// stubShow 替换平台实现，让 Run 的流程可以在无 GUI 环境里被测。
func stubShow(t *testing.T, res Result, retErr error, seen *[]Item) {
	t.Helper()
	prev := showFn
	showFn = func(items []Item, opts Options) (Result, error) {
		if seen != nil {
			*seen = items
		}
		return res, retErr
	}
	t.Cleanup(func() { showFn = prev })
}

func TestRunEmptyHistory(t *testing.T) {
	stubShow(t, Result{OK: true, ID: "whatever"}, nil, nil)
	err := Run(nil, func(string) error { return nil }, "dev-self", Options{})
	if err == nil || !strings.Contains(err.Error(), "为空") {
		t.Fatalf("空历史应返回可读错误，实际 %v", err)
	}
}

func TestRunPullsSelectedID(t *testing.T) {
	stubShow(t, Result{OK: true, ID: "id2"}, nil, nil)
	var pulled string
	err := Run(entries(), func(id string) error { pulled = id; return nil }, "dev-self", Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pulled != "id2" {
		t.Fatalf("应取回 id2，实际 %q", pulled)
	}
}

func TestRunCancelDoesNotPull(t *testing.T) {
	stubShow(t, Result{OK: false}, nil, nil)
	pulled := false
	err := Run(entries(), func(string) error { pulled = true; return nil }, "dev-self", Options{})
	if err != nil {
		t.Fatalf("取消不应报错: %v", err)
	}
	if pulled {
		t.Fatal("取消时不得取回任何条目")
	}
}

func TestRunPropagatesShowError(t *testing.T) {
	stubShow(t, Result{}, errors.New("呈现失败"), nil)
	if err := Run(entries(), func(string) error { return nil }, "dev-self", Options{}); err == nil {
		t.Fatal("show 的错误应向上传递")
	}
}

func TestRunPropagatesPullError(t *testing.T) {
	stubShow(t, Result{OK: true, ID: "id1"}, nil, nil)
	wantErr := errors.New("写剪贴板失败")
	if err := Run(entries(), func(string) error { return wantErr }, "dev-self", Options{}); !errors.Is(err, wantErr) {
		t.Fatalf("pull 错误应向上传递，实际 %v", err)
	}
}

func TestRunAppliesLimitAndPassesItemsToShow(t *testing.T) {
	var seen []Item
	stubShow(t, Result{OK: false}, nil, &seen)
	if err := Run(entries(), func(string) error { return nil }, "dev-self", Options{Limit: 2}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 2 {
		t.Fatalf("应只把前 2 条交给窗口，实际 %d 条", len(seen))
	}
}

func TestRunDefaultLimit(t *testing.T) {
	var seen []Item
	stubShow(t, Result{OK: false}, nil, &seen)
	if err := Run(entries(), func(string) error { return nil }, "dev-self", Options{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("条目数少于上限时应全部展示，实际 %d", len(seen))
	}
}
