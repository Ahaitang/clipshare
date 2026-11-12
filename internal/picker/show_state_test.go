package picker

import (
	"fmt"
	"strings"
	"testing"
)

// 这些测试跑的是 show_state.go 里的纯逻辑，不需要任何图形环境，
// 因此 Linux CI 与 Windows 上都照样跑（这正是把逻辑抽出来的目的）。

// sampleItems 返回一组便于断言的条目：4 条，内容/来源里埋了不同关键词。
func sampleItems() []Item {
	return []Item{
		{ID: "id1", Source: "本机", Content: "alpha 第一条", When: "11-11 09:00"},
		{ID: "id2", Source: "laptop", Content: "beta 第二条", When: "11-11 10:00"},
		{ID: "id3", Source: "本机", Content: "alpha 第三条", When: "11-11 11:00"},
		{ID: "id4", Source: "nas", Content: "gamma 第四条", When: "11-11 12:00"},
	}
}

// manyItems 返回 n 条可区分的条目，用于验证滚动与翻页（n 通常大于一屏）。
func manyItems(n int) []Item {
	items := make([]Item, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, Item{
			ID:      fmt.Sprintf("id-%02d", i),
			Source:  "本机",
			Content: fmt.Sprintf("第 %d 条", i),
			When:    "11-11 10:00",
		})
	}
	return items
}

func TestUILayoutFitsWindow(t *testing.T) {
	if uiVisibleRows < 1 {
		t.Fatalf("列表区必须至少能显示 1 行，得到 %d", uiVisibleRows)
	}
	if uiPageStep < 1 {
		t.Fatalf("翻页步长必须至少 1 行，得到 %d", uiPageStep)
	}
	// 最后一个完整可见行必须落在列表区内，否则会画到分隔线/底部统计上。
	lastBottom := uiRowTop(uiVisibleRows-1) + uiRowH
	if lastBottom > uiListBottom {
		t.Fatalf("最后一行底部 %d 超出列表区下边界 %d", lastBottom, uiListBottom)
	}
	if uiListBottom > uiHeight-uiPad {
		t.Fatalf("列表区下边界 %d 超出窗口内容区 %d", uiListBottom, uiHeight-uiPad)
	}
}

func TestUIStateInitial(t *testing.T) {
	items := sampleItems()
	s := newUIState(items)

	if s.count() != len(items) || s.total() != len(items) {
		t.Fatalf("初始应显示全部条目：count=%d total=%d", s.count(), s.total())
	}
	if s.sel != 0 || s.scroll != 0 {
		t.Fatalf("初始选中/滚动应为 0，得到 sel=%d scroll=%d", s.sel, s.scroll)
	}
	if it, ok := s.highlighted(); !ok || it.ID != "id1" {
		t.Fatalf("初始高亮应是第一条，得到 %+v ok=%v", it, ok)
	}
	if got := s.queryText(); got != "" {
		t.Fatalf("初始搜索词应为空，得到 %q", got)
	}
}

func TestUIStateCharInputFiltersAndResetsSelection(t *testing.T) {
	s := newUIState(sampleItems())
	s.selectIndex(3)
	if s.sel != 3 {
		t.Fatalf("先选中第 4 条，得到 sel=%d", s.sel)
	}

	for _, r := range "alpha" {
		if res, act := s.handleKey(uiKeyChar, r); act != uiActionRedraw || res.OK {
			t.Fatalf("输入 %q 时不应确认/取消，得到 act=%v res=%+v", r, act, res)
		}
	}

	// "alpha" 命中 id1、id3（大小写不敏感的子串匹配，见 Filter）。
	if s.count() != 2 {
		t.Fatalf(`输入 "alpha" 后应剩 2 条，得到 %d: %+v`, s.count(), s.shown)
	}
	// 输入后选中必须归零：过滤改变了列表顺序，保留旧下标会取回一条没看清的条目。
	if s.sel != 0 || s.scroll != 0 {
		t.Fatalf(`输入后选中/滚动应归零，得到 sel=%d scroll=%d`, s.sel, s.scroll)
	}
	if s.queryText() != "alpha" {
		t.Fatalf("搜索词应累积为 alpha，得到 %q", s.queryText())
	}
}

func TestUIStateCharInputIsCaseInsensitive(t *testing.T) {
	s := newUIState(sampleItems())
	for _, r := range "ALPHA" {
		s.handleKey(uiKeyChar, r)
	}
	if s.count() != 2 {
		t.Fatalf("大写搜索词也应命中 2 条，得到 %d", s.count())
	}
}

func TestUIStateCharInputMatchesSource(t *testing.T) {
	s := newUIState(sampleItems())
	for _, r := range "nas" {
		s.handleKey(uiKeyChar, r)
	}
	if s.count() != 1 || s.shown[0].ID != "id4" {
		t.Fatalf("按来源过滤应只剩 id4，得到 %+v", s.shown)
	}
}

func TestUIStateCharInputMatchesCJK(t *testing.T) {
	s := newUIState(sampleItems())
	for _, r := range "第三条" {
		s.handleKey(uiKeyChar, r)
	}
	if s.count() != 1 || s.shown[0].ID != "id3" {
		t.Fatalf("中文搜索应只剩 id3，得到 %+v", s.shown)
	}
	// 退格删的是「一个字符」而不是一个字节，否则中文搜索词会被切碎。
	s.handleKey(uiKeyBackspace, 0)
	if got := s.queryText(); got != "第三" {
		t.Fatalf("退格应删掉一个汉字，得到 %q", got)
	}
}

func TestUIStateBackspaceRestoresFullList(t *testing.T) {
	s := newUIState(sampleItems())
	for _, r := range "beta" {
		s.handleKey(uiKeyChar, r)
	}
	if s.count() != 1 {
		t.Fatalf(`输入 "beta" 后应剩 1 条，得到 %d`, s.count())
	}
	for range "beta" {
		s.handleKey(uiKeyBackspace, 0)
	}
	if s.count() != 4 {
		t.Fatalf("搜索词删空后应恢复全部 4 条，得到 %d", s.count())
	}
	// 搜索词已空时再退格：空操作，不能 panic、也不能退出。
	s.handleKey(uiKeyBackspace, 0)
	if _, act := s.handleKey(uiKeyBackspace, 0); act != uiActionRedraw {
		t.Fatalf("空搜索词上退格不应触发关闭，得到 act=%v", act)
	}
}

func TestUIStateIgnoresControlChars(t *testing.T) {
	s := newUIState(sampleItems())
	// Backspace/Enter/Esc/Tab 会同时以 WM_KEYDOWN 与 WM_CHAR 到达；
	// 若是漏进搜索词，就会变成永久匹配不到内容的隐藏字符。
	for _, r := range []rune{'\n', '\r', '\t', 0x1b, 0x08, 0x7f} {
		s.insertRune(r)
	}
	if s.queryText() != "" {
		t.Fatalf("控制字符不应进入搜索词，得到 %q", s.queryText())
	}
	// 空格是可打印字符，必须能被搜索。
	s.insertRune(' ')
	if s.queryText() != " " {
		t.Fatalf("空格应可进入搜索词，得到 %q", s.queryText())
	}
}

func TestUIStateArrowKeysClampAtBoundaries(t *testing.T) {
	s := newUIState(sampleItems())

	s.handleKey(uiKeyUp, 0)
	if s.sel != 0 {
		t.Fatalf("在第一行按 ↑ 应夹紧在 0，得到 %d", s.sel)
	}
	s.handleKey(uiKeyEnd, 0)
	if s.sel != 3 {
		t.Fatalf("End 应选中最后一条 3，得到 %d", s.sel)
	}
	s.handleKey(uiKeyDown, 0)
	if s.sel != 3 {
		t.Fatalf("在最后一行按 ↓ 应夹紧在 3，得到 %d", s.sel)
	}
	s.handleKey(uiKeyHome, 0)
	if s.sel != 0 {
		t.Fatalf("Home 应选中 0，得到 %d", s.sel)
	}
}

func TestUIStateEmptyListNeverPanics(t *testing.T) {
	s := newUIState(sampleItems())
	for _, r := range "没有这个条目" {
		s.handleKey(uiKeyChar, r)
	}
	if s.count() != 0 {
		t.Fatalf("该搜索词不该命中任何条目，得到 %d", s.count())
	}
	for _, k := range []uiKey{uiKeyUp, uiKeyDown, uiKeyPageUp, uiKeyPageDown, uiKeyHome, uiKeyEnd} {
		if _, act := s.handleKey(k, 0); act != uiActionRedraw {
			t.Fatalf("空列表上的移动键不应关闭窗口，key=%v act=%v", k, act)
		}
	}
	if s.sel != 0 || s.scroll != 0 {
		t.Fatalf("空列表上选中/滚动必须归零，得到 sel=%d scroll=%d", s.sel, s.scroll)
	}
	if _, ok := s.highlighted(); ok {
		t.Fatalf("空列表上不应有高亮条目")
	}
	// 空列表上按 Enter：不确认，只是重绘（平台层可以顺手响一声）。
	if res, act := s.handleKey(uiKeyEnter, 0); act != uiActionRedraw || res.OK || res.ID != "" {
		t.Fatalf("空列表上 Enter 不应确认，得到 act=%v res=%+v", act, res)
	}

	// 全空的 uiState 也不能 panic。
	empty := newUIState(nil)
	empty.handleKey(uiKeyPageDown, 0)
	empty.scrollBy(5)
	if _, ok := empty.selectAt(uiListTop); ok {
		t.Fatalf("空列表上点击不应命中任何行")
	}
}

func TestUIStatePageKeys(t *testing.T) {
	s := newUIState(manyItems(20))

	s.handleKey(uiKeyPageDown, 0)
	if s.sel != uiPageStep {
		t.Fatalf("第一次 PgDn 应到 %d，得到 %d", uiPageStep, s.sel)
	}
	s.handleKey(uiKeyPageDown, 0)
	if s.sel != 19 {
		t.Fatalf("第二次 PgDn 应夹紧到最后一条 19，得到 %d", s.sel)
	}
	s.handleKey(uiKeyPageUp, 0)
	if want := 19 - uiPageStep; s.sel != want {
		t.Fatalf("PgUp 应回到 %d，得到 %d", want, s.sel)
	}
	s.handleKey(uiKeyPageUp, 0)
	if s.sel != 0 {
		t.Fatalf("再 PgUp 应夹紧到 0，得到 %d", s.sel)
	}
}

func TestUIStateSelectionStaysVisible(t *testing.T) {
	s := newUIState(manyItems(20))
	for i := 0; i < 15; i++ {
		s.handleKey(uiKeyDown, 0)
	}
	if s.sel != 15 {
		t.Fatalf("连按 15 次 ↓ 应到 15，得到 %d", s.sel)
	}
	if s.sel < s.scroll || s.sel >= s.scroll+uiVisibleRows {
		t.Fatalf("高亮行必须留在视口内：sel=%d scroll=%d 可见行数=%d", s.sel, s.scroll, uiVisibleRows)
	}
	// 20 条、一屏 14 条 → 最大滚动 6。
	if s.scroll != 2 {
		t.Fatalf("高亮到第 15 条时滚动应为 2，得到 %d", s.scroll)
	}
	// 回到顶部时视口也要跟着回顶。
	s.handleKey(uiKeyHome, 0)
	if s.scroll != 0 {
		t.Fatalf("Home 后应滚回顶部，得到 scroll=%d", s.scroll)
	}
	s.handleKey(uiKeyPageDown, 0)
	s.handleKey(uiKeyPageDown, 0)
	if s.scroll != s.maxScroll() {
		t.Fatalf("滚到底时应停在 maxScroll=%d，得到 scroll=%d", s.maxScroll(), s.scroll)
	}
}

func TestUIStateWheelScroll(t *testing.T) {
	s := newUIState(manyItems(20))

	// 滚轮向下 3 格：视口与高亮一起下移，Enter 才取回屏幕上看的那一行。
	s.scrollBy(3)
	if s.scroll != 3 || s.sel != 3 {
		t.Fatalf("下滚 3 格应 scroll=3 sel=3，得到 scroll=%d sel=%d", s.scroll, s.sel)
	}
	// 向上滚过头：夹紧在顶部。
	s.scrollBy(-10)
	if s.scroll != 0 || s.sel != 0 {
		t.Fatalf("上滚过头应夹紧到 0，得到 scroll=%d sel=%d", s.scroll, s.sel)
	}
	// 向下滚过头：停在 maxScroll，高亮停在最后一条。
	s.scrollBy(100)
	if s.scroll != s.maxScroll() {
		t.Fatalf("下滚过头应停在 maxScroll=%d，得到 scroll=%d", s.maxScroll(), s.scroll)
	}
	if s.sel != 19 {
		t.Fatalf("下滚到底时高亮应是最后一条 19，得到 %d", s.sel)
	}
	// 滚轮在最顶端时不该动，也不该 panic。
	s.selectIndex(0)
	s.scrollBy(0)
	if s.scroll != 0 {
		t.Fatalf("0 格滚动不该改变视口，得到 %d", s.scroll)
	}
}

func TestUIStateClickMapsYToRow(t *testing.T) {
	s := newUIState(manyItems(20))

	// 点第 3 个可见行的中间位置 → 取回第 3 条（下标 2）。
	y := uiRowTop(2) + uiRowH/2
	it, ok := s.selectAt(y)
	if !ok || it.ID != "id-02" || s.sel != 2 {
		t.Fatalf("点第 3 行应命中 id-02，得到 %+v ok=%v sel=%d", it, ok, s.sel)
	}

	// 滚动后同样的 y 应命中不同的条目（行号 = scroll + slot）。
	s.scrollBy(5)
	it, ok = s.selectAt(y)
	if !ok || it.ID != "id-07" {
		t.Fatalf("滚动 5 行后点第 3 行应命中 id-07，得到 %+v ok=%v", it, ok)
	}

	// 点击标题/搜索区、底部统计、列表末尾余量、列表末尾之后的空白：都不算命中，
	// 否则点一下底部统计就会误取回条目。
	for _, badY := range []int{
		uiPad,                            // 标题行
		uiListTop - 1,                    // 搜索区
		uiListTop + uiVisibleRows*uiRowH, // 列表区末尾不满一行的余量
		uiListBottom,                     // 底部统计行
		uiHeight,
		-1,
	} {
		if _, ok := s.selectAt(badY); ok {
			t.Fatalf("y=%d 不在任何可见行内，不该命中", badY)
		}
	}

	// 列表只有 4 条时，点第 10 个槽位不该命中（那里没有条目）。
	short := newUIState(sampleItems())
	if _, ok := short.selectAt(uiRowTop(10) + uiRowH/2); ok {
		t.Fatalf("列表只有 4 条时点第 11 个槽位不该命中")
	}
}

func TestUIStateRowSlotBoundaries(t *testing.T) {
	cases := []struct {
		y    int
		n    int
		want bool
	}{
		{uiListTop, 0, true},
		{uiListTop + uiRowH - 1, 0, true},
		{uiListTop + uiRowH, 1, true},
		{uiListTop - 1, 0, false},
		{uiListTop + uiVisibleRows*uiRowH - 1, uiVisibleRows - 1, true},
		{uiListTop + uiVisibleRows*uiRowH, 0, false},
		{uiListBottom - 1, 0, false},
		{uiListBottom, 0, false},
	}
	for _, c := range cases {
		slot, ok := uiRowSlot(c.y)
		if ok != c.want {
			t.Fatalf("y=%d：想要 ok=%v，得到 ok=%v slot=%d", c.y, c.want, ok, slot)
		}
		if ok && slot != c.n {
			t.Fatalf("y=%d：想要 slot=%d，得到 %d", c.y, c.n, slot)
		}
	}
}

func TestUIStateHandleKeyConfirmAndCancel(t *testing.T) {
	s := newUIState(sampleItems())

	s.handleKey(uiKeyDown, 0)
	res, act := s.handleKey(uiKeyEnter, 0)
	if act != uiActionConfirm || !res.OK || res.ID != "id2" {
		t.Fatalf("Enter 应确认高亮行 id2，得到 act=%v res=%+v", act, res)
	}

	// Esc：取消，Result 的 OK 必须为 false（Run 靠它区分「用户取消」与「取回失败」）。
	res, act = s.handleKey(uiKeyEscape, 0)
	if act != uiActionCancel || res.OK || res.ID != "" {
		t.Fatalf("Esc 应取消并返回空 Result，得到 act=%v res=%+v", act, res)
	}

	// 过滤后再 Enter：取回的是过滤结果里的高亮行。
	for _, r := range "nas" {
		s.handleKey(uiKeyChar, r)
	}
	res, act = s.handleKey(uiKeyEnter, 0)
	if act != uiActionConfirm || res.ID != "id4" {
		t.Fatalf("过滤后 Enter 应取回 id4，得到 act=%v res=%+v", act, res)
	}
}

func TestUIRowText(t *testing.T) {
	body, meta := uiRowText(Item{Source: "本机", Content: "第一行\n第二行", When: "11-11 10:00"})
	if !strings.Contains(body, "⏎") {
		t.Fatalf("多行内容应折叠成单行（含 ⏎），得到 %q", body)
	}
	if meta != "本机 · 11-11 10:00" {
		t.Fatalf("次信息应为「来源 · 时间」，得到 %q", meta)
	}

	// When 为空时不要留一个孤零零的分隔符。
	if _, meta := uiRowText(Item{Source: "nas"}); meta != "nas" {
		t.Fatalf("没有时间时次信息应只有来源，得到 %q", meta)
	}

	// 空内容给占位，避免画出一条看不见的空行。
	if body, _ := uiRowText(Item{}); body != "(空内容)" {
		t.Fatalf("空内容应有占位文案，得到 %q", body)
	}

	// 超长内容按下限截断并带省略号。
	long := strings.Repeat("很长的内容", 100)
	body2, _ := uiRowText(Item{Content: long})
	if n := len([]rune(body2)); n > uiRowPreviewRunes+1 {
		t.Fatalf("预览应截断到 %d rune 以内（含省略号），得到 %d", uiRowPreviewRunes+1, n)
	}
	if !strings.HasSuffix(body2, "…") {
		t.Fatalf("截断后应以省略号结尾，得到 %q", body2)
	}
}

func TestUISurrogateFeed(t *testing.T) {
	var b uiSurrogate

	// BMP 字符直接返回。
	if r, ok := b.feed('a'); !ok || r != 'a' {
		t.Fatalf("普通字符应立即返回，得到 %q ok=%v", r, ok)
	}

	// 非 BMP（U+1F600 😀）在 Windows 上分成高低两个代理项到达。
	if r, ok := b.feed(0xd83d); ok || r != 0 {
		t.Fatalf("高代理项不该单独产出字符，得到 %q ok=%v", r, ok)
	}
	r, ok := b.feed(0xde00)
	if !ok || r != '😀' {
		t.Fatalf("高低代理项应拼回 😀，得到 %q ok=%v", r, ok)
	}

	// 孤立的高代理项后面跟普通字符：不该吞掉后面的普通字符。
	b.feed(0xd83d)
	if r, ok := b.feed('b'); !ok || r != 'b' {
		t.Fatalf("孤立高代理项后的字符必须照常返回，得到 %q ok=%v", r, ok)
	}

	// 孤立的低代理项丢弃。
	if _, ok := b.feed(0xdc00); ok {
		t.Fatalf("孤立低代理项应被丢弃")
	}
}

func TestUIStateQueryLineAndSummary(t *testing.T) {
	s := newUIState(sampleItems())
	if q := s.queryLine(); !strings.Contains(q, "搜索") {
		t.Fatalf("空搜索词应给占位提示，得到 %q", q)
	}
	if sum := s.summary(); !strings.Contains(sum, "1/4") {
		t.Fatalf("底部统计应给出当前位置，得到 %q", sum)
	}

	// 'b' 只命中 "beta 第二条"：用来验证「共 N 条 · 匹配 M 条」这段文案。
	s.insertRune('b')
	if q := s.queryLine(); !strings.Contains(q, "：b") {
		t.Fatalf("搜索行应显示搜索词，得到 %q", q)
	}
	if sum := s.summary(); !strings.Contains(sum, "匹配") {
		t.Fatalf("过滤后统计应说明匹配条数，得到 %q", sum)
	}

	for _, r := range "zzz" {
		s.insertRune(r)
	}
	if sum := s.summary(); !strings.Contains(sum, "无匹配") {
		t.Fatalf("无匹配时统计应明确说明，得到 %q", sum)
	}

	emptySummary := newUIState(nil).summary()
	if !strings.Contains(emptySummary, "没有可用的历史条目") {
		t.Fatalf("空列表统计应给出说明，得到 %q", emptySummary)
	}
}

func TestUITitle(t *testing.T) {
	if got := uiTitle(Options{}); got != "剪贴板历史" {
		t.Fatalf("默认标题应稳定，得到 %q", got)
	}
	if got := uiTitle(Options{Title: "  自定义标题  "}); got != "自定义标题" {
		t.Fatalf("自定义标题应去掉两端空白，得到 %q", got)
	}
}

func TestUIFaceMatches(t *testing.T) {
	cases := []struct {
		requested string
		realized  string
		want      bool
		why       string
	}{
		{"Microsoft YaHei UI", "Microsoft YaHei UI", true, "完全一致"},
		{"Microsoft YaHei UI", "microsoft yahei ui", true, "大小写不敏感"},
		{"Microsoft YaHei UI", "Microsoft YaHei", true, "老系统没有 UI 变体，给回同族字体"},
		{"Segoe UI", "Segoe UI Semibold", true, "实际给回同族的更细变体"},
		{"Microsoft YaHei UI", "MS Shell Dlg", false, "被替换成系统默认字体：说明请求的字体没装"},
		{"Microsoft YaHei UI", "微软雅黑", false, "本地化字体名对不上，调用方要保留可用的候选"},
		{"Microsoft YaHei UI", "", false, "问不出实际字体名时不能当成匹配"},
		{"", "Segoe UI", false, "空请求不能因为前缀比较而误判为匹配"},
	}
	for _, c := range cases {
		if got := uiFaceMatches(c.requested, c.realized); got != c.want {
			t.Fatalf("uiFaceMatches(%q, %q) = %v，想要 %v（%s）", c.requested, c.realized, got, c.want, c.why)
		}
	}
}
