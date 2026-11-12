// 本文件是选择窗口的「纯状态」：布局几何、搜索词编辑、选中/滚动、按键语义、
// 行文案。它不引用任何平台 API，因此在任何平台都能编译、都能单测（Linux CI 上也跑）。
//
// 平台实现（show_windows.go 的 Win32 自绘窗口、以及将来的 X11 自绘窗口）只做两件事：
// 把本地键码/鼠标坐标翻译成这里的 uiKey 与 y 坐标，把这里算出的状态画出来。
// 这样「过滤语义、方向键行为、行号计算」只有一份实现，不会各平台漂移。

package picker

import (
	"fmt"
	"strings"
	"unicode/utf16"
)

// 窗口与行的固定布局（像素）。Windows 侧按这些尺寸建窗口，X11 侧同理。
const (
	uiWidth   = 640
	uiHeight  = 430
	uiPad     = 16 // 四周留白
	uiTitleH  = 26 // 标题行
	uiQueryH  = 30 // 搜索行
	uiRowH    = 22 // 单条列表行
	uiFooterH = 22 // 底部统计行
)

// 列表区的上下边界（客户区坐标）。两者之间按 uiRowH 切行。
const (
	uiListTop    = uiPad + uiTitleH + uiQueryH
	uiListBottom = uiHeight - uiPad - uiFooterH
)

// uiVisibleRows 是列表区一次能完整显示的行数（不完整的那一截留给底部统计）。
const uiVisibleRows = (uiListBottom - uiListTop) / uiRowH

// uiPageStep 是 PgUp/PgDn 的步长：比一屏少一行，翻页后还能看到上一屏的最后一条，
// 避免用户「翻过头不知道自己看到哪」。
const uiPageStep = uiVisibleRows - 1

// uiRowPreviewRunes 是单行预览按 rune 截断的上限。像素宽度由平台绘制层
// （DrawText 的省略号）再兜一层，这里只防止超长行拖慢渲染。
const uiRowPreviewRunes = 60

// uiEmptyHint 是「有历史但当前搜索词没有命中」时画在列表区中央的提示。
const uiEmptyHint = "没有匹配的条目（Backspace 修改搜索词，Esc 取消）"

// uiRowTop 返回第 slot 个可见行的上边界（客户区坐标）。
func uiRowTop(slot int) int { return uiListTop + slot*uiRowH }

// uiRowSlot 把客户区 y 坐标映射成「列表区第几个可见行」（0 = 最上面一行）。
//
// ok=false 表示该 y 不在任何完整可见行内：标题区、搜索区、底部统计、
// 或列表区最后那截不满一行的余量。点击落在这些地方应当被忽略，而不是
// 被算成「最后一行」——否则点一下底部统计就会误取回条目。
func uiRowSlot(y int) (slot int, ok bool) {
	if y < uiListTop || y >= uiListBottom {
		return 0, false
	}
	slot = (y - uiListTop) / uiRowH
	if slot >= uiVisibleRows {
		return 0, false
	}
	return slot, true
}

// uiClamp 把 v 夹在 [lo, hi] 内。
func uiClamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// uiState 是一个选择窗口的全部可变状态。
//
// 字段都是「意图」而不是「像素」：all 是完整列表，shown 是过滤后的列表，
// scroll 是列表区第一行在 shown 里的下标。平台层只读它来绘制。
type uiState struct {
	all    []Item // 完整列表（Filter 的输入，不可变）
	shown  []Item // 当前过滤结果，sel/scroll 都相对它
	query  []rune // 搜索词，按 rune 存，退格删的是「一个字符」而不是一个字节
	sel    int    // 高亮行在 shown 中的下标
	scroll int    // 视口第一行在 shown 中的下标
}

// newUIState 以完整列表创建一个初始状态：高亮第一条、不滚动、无搜索词。
func newUIState(items []Item) *uiState {
	return &uiState{all: items, shown: items}
}

// queryText 返回当前搜索词。
func (s *uiState) queryText() string { return string(s.query) }

// count 返回过滤后的条目数。
func (s *uiState) count() int { return len(s.shown) }

// total 返回完整列表的条目数（用于「共 N 条 / 匹配 M 条」）。
func (s *uiState) total() int { return len(s.all) }

// highlighted 返回当前高亮条目；列表为空时 ok=false。
func (s *uiState) highlighted() (Item, bool) {
	if s.sel < 0 || s.sel >= len(s.shown) {
		return Item{}, false
	}
	return s.shown[s.sel], true
}

// maxScroll 是滚动偏移的上限：列表末尾之后不能再滚。
func (s *uiState) maxScroll() int {
	if n := len(s.shown) - uiVisibleRows; n > 0 {
		return n
	}
	return 0
}

// clampScroll 把 scroll/sel 收进合法区间，并保证高亮行一定在视口内。
//
// 「高亮必须在视口内」是交互正确性要求：用户按 Enter 取回的是高亮那一条，
// 如果它被滚出屏幕，用户就看不见自己将要取回什么。
func (s *uiState) clampScroll() {
	if len(s.shown) == 0 {
		s.sel = 0
		s.scroll = 0
		return
	}
	s.sel = uiClamp(s.sel, 0, len(s.shown)-1)
	s.scroll = uiClamp(s.scroll, 0, s.maxScroll())
	if s.sel < s.scroll {
		s.scroll = s.sel
	}
	if s.sel >= s.scroll+uiVisibleRows {
		s.scroll = s.sel - uiVisibleRows + 1
	}
	s.scroll = uiClamp(s.scroll, 0, s.maxScroll())
}

// refilter 用当前搜索词重新过滤，并把选中与滚动归零。
//
// 归零而不是「尽量保持原来选的那条」：搜索词变化后列表顺序与内容都变了，
// 保持旧下标会让用户按 Enter 时取回一条他没看清的条目。
func (s *uiState) refilter() {
	s.shown = Filter(s.all, s.queryText())
	s.sel = 0
	s.scroll = 0
}

// insertRune 把可打印字符追加到搜索词末尾；控制字符（换行、Tab、DEL 等）忽略。
func (s *uiState) insertRune(r rune) {
	if !uiPrintable(r) {
		return
	}
	s.query = append(s.query, r)
	s.refilter()
}

// backspace 删掉搜索词最后一个字符；搜索词已空时是空操作（不 panic、不退出）。
func (s *uiState) backspace() {
	if len(s.query) == 0 {
		return
	}
	s.query = s.query[:len(s.query)-1]
	s.refilter()
}

// uiPrintable 判断一个 rune 是否算「搜索词字符」。
//
// 排除 C0 控制字符与 DEL：Backspace/Enter/Esc/Tab 会同时以 WM_KEYDOWN 和
// WM_CHAR 到达，如果不在这里挡掉，它们会被当成搜索词的一部分写进 query。
func uiPrintable(r rune) bool {
	if r < 0x20 || r == 0x7f {
		return false
	}
	return true
}

// moveBy 按 delta 移动高亮行，边界处夹紧（复用 Filter/MoveIndex 的共享实现）。
func (s *uiState) moveBy(delta int) {
	s.sel = MoveIndex(s.sel, delta, len(s.shown))
	s.clampScroll()
}

// selectIndex 把高亮行设为 i（夹紧），并滚动到能看见它。
func (s *uiState) selectIndex(i int) {
	if len(s.shown) == 0 {
		s.sel = 0
		s.scroll = 0
		return
	}
	s.sel = uiClamp(i, 0, len(s.shown)-1)
	s.clampScroll()
}

// scrollBy 按行滚动视口（滚轮）：高亮行跟着视口一起动，
// 保证「按 Enter 取回」的永远是屏幕上高亮的那一行。
func (s *uiState) scrollBy(rows int) {
	if rows == 0 || len(s.shown) == 0 {
		return
	}
	s.scroll += rows
	s.sel += rows
	s.clampScroll()
}

// selectAt 把鼠标点击的客户区 y 坐标换算成条目并高亮它。
//
// ok=false 表示点击没有落在任何可见行上（或落在列表末尾的空白里）。
// 只取 y：横向点击同一行的任何位置都算命中，符合「点一行就取回」的直觉。
func (s *uiState) selectAt(y int) (Item, bool) {
	slot, ok := uiRowSlot(y)
	if !ok {
		return Item{}, false
	}
	idx := s.scroll + slot
	if idx < 0 || idx >= len(s.shown) {
		return Item{}, false
	}
	s.sel = idx
	s.clampScroll()
	return s.shown[idx], true
}

// uiKey 是平台无关的按键语义。平台层负责把本地键码翻译成它：
// Windows 侧是 VK_xxx，X11 侧是 keysym，都只映射到下面这几个值。
type uiKey int

const (
	uiKeyUnknown uiKey = iota
	uiKeyUp
	uiKeyDown
	uiKeyPageUp
	uiKeyPageDown
	uiKeyHome
	uiKeyEnd
	uiKeyEnter
	uiKeyEscape
	uiKeyBackspace
	uiKeyChar // 可打印字符，字符本体从 handleKey 的 r 参数取
)

// uiAction 是处理一次按键后平台层该做的事。
type uiAction int

const (
	uiActionRedraw  uiAction = iota // 只是状态变了，重绘即可
	uiActionConfirm                 // 关闭窗口，返回带 ID 的 Result
	uiActionCancel                  // 关闭窗口，返回 OK=false
)

// handleKey 处理一次按键，返回（仅 confirm 时有意义的）结果与下一步动作。
//
// 列表为空时按 Enter 不做任何事（平台层可以顺手响一声提示音），
// 这样用户不会误以为「我取回了一条空记录」。
func (s *uiState) handleKey(k uiKey, r rune) (Result, uiAction) {
	switch k {
	case uiKeyUp:
		s.moveBy(-1)
	case uiKeyDown:
		s.moveBy(1)
	case uiKeyPageUp:
		s.moveBy(-uiPageStep)
	case uiKeyPageDown:
		s.moveBy(uiPageStep)
	case uiKeyHome:
		s.selectIndex(0)
	case uiKeyEnd:
		s.selectIndex(s.count() - 1)
	case uiKeyEnter:
		if it, ok := s.highlighted(); ok {
			return Result{ID: it.ID, OK: true}, uiActionConfirm
		}
	case uiKeyEscape:
		return Result{}, uiActionCancel
	case uiKeyBackspace:
		s.backspace()
	case uiKeyChar:
		s.insertRune(r)
	}
	return Result{}, uiActionRedraw
}

// uiSurrogate 把 UTF-16 码元流拼回完整 rune。
//
// Windows 的 WM_CHAR 每次只给一个 UTF-16 码元，所以 emoji 之类非 BMP 字符
// 会分两条消息到达（高代理 + 低代理）。直接 append 半个代理项会让搜索词
// 里出现 U+FFFD，永远匹配不到东西。逻辑放这里是为了能在 Linux 上单测。
type uiSurrogate struct{ high uint16 }

// feed 吃掉一个码元；凑成完整字符时返回 (rune, true)。
func (b *uiSurrogate) feed(unit uint16) (rune, bool) {
	switch {
	case unit >= 0xd800 && unit <= 0xdbff: // 高代理项：等下一个码元
		b.high = unit
		return 0, false
	case unit >= 0xdc00 && unit <= 0xdfff: // 低代理项
		high := b.high
		b.high = 0
		if high == 0 {
			return 0, false // 孤立的低代理项：丢弃
		}
		return utf16.DecodeRune(rune(high), rune(unit)), true
	default:
		// 普通字符：如果之前挂着一个没配上的高代理项，直接丢弃它。
		b.high = 0
		return rune(unit), true
	}
}

// title 返回窗口标题文案：调用方没给标题时用默认文案。
func uiTitle(opts Options) string {
	if t := strings.TrimSpace(opts.Title); t != "" {
		return t
	}
	return "剪贴板历史"
}

// queryLine 生成搜索行文案；搜索词为空时给占位提示。
func (s *uiState) queryLine() string {
	if len(s.query) == 0 {
		return "搜索（键入以过滤）："
	}
	return "搜索：" + string(s.query)
}

// summary 生成底部统计行文案。
func (s *uiState) summary() string {
	total, shown := s.total(), s.count()
	switch {
	case total == 0:
		return "没有可用的历史条目 · Esc 关闭"
	case shown == 0:
		return fmt.Sprintf("共 %d 条 · 无匹配 · Backspace 改搜索词 · Esc 取消", total)
	case shown != total:
		return fmt.Sprintf("共 %d 条 · 匹配 %d 条 · 第 %d/%d 条 · Enter 取回 · Esc 取消", total, shown, s.sel+1, shown)
	default:
		return fmt.Sprintf("共 %d 条 · 第 %d/%d 条 · Enter 取回 · Esc 取消", total, s.sel+1, shown)
	}
}

// uiRowText 把一条记录拆成「左对齐的主体」与「右对齐的次要信息」两段。
//
// 主体是单行预览（换行折叠成 ⏎），次要信息是「来源 · 时间」。
// 拆成两段是为了让平台层能把时间固定对齐在右边，而不是随内容长度乱跑。
func uiRowText(it Item) (body, meta string) {
	body = Preview(it.Content, uiRowPreviewRunes)
	if body == "" {
		body = "(空内容)"
	}
	meta = it.Source
	if it.When != "" {
		meta = it.Source + " · " + it.When
	}
	return body, meta
}

// uiFaceMatches 宽松比较「请求的字体族名」与「实际生效的字体族名」。
//
// Windows 侧用它实现「YaHei UI → Segoe UI → 默认 GUI 字体」的降级判断：
// 允许前缀关系，因为在中文系统上请求 "Microsoft YaHei UI" 时 GDI 可能给回
// "Microsoft YaHei"（老系统没有 UI 变体），那是可接受的结果；
// 但给回 "MS Shell Dlg" 就说明请求的字体确实没装，该换下一个候选。
//
// 放在本文件是因为它是纯字符串判断，能在 Linux 上单测——字体降级这条链路
// 是本包在真实 Windows 上最不容易一眼看出的部分。
func uiFaceMatches(requested, realized string) bool {
	req := strings.ToLower(strings.TrimSpace(requested))
	got := strings.ToLower(strings.TrimSpace(realized))
	if req == "" || got == "" {
		return false
	}
	if req == got {
		return true
	}
	return strings.HasPrefix(req, got) || strings.HasPrefix(got, req)
}
