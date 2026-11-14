// Package tray 实现系统托盘界面（design §9）。
//
// 它只依赖 engine.Controller 契约：渲染时读 ctrl.Snapshot()，菜单回调里调控制器
// 方法，不 import engine 的任何实现。
//
// 关键实现约束：github.com/energye/systray 只有 ResetMenu（清空重建，会闪），
// 没有 RemoveMenuItem。因此菜单在启动时一次性建好固定条目（含 10 个历史槽位），
// 之后只用 SetTitle/Show/Hide/Enable/Disable/Check/Uncheck 做增量更新，
// 全程不调用 ResetMenu。
package tray

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/energye/systray"

	"clipshare/internal/engine"
	"clipshare/internal/model"
)

// Options 是托盘运行参数。
type Options struct {
	// UIAddr 是本地历史页地址，如 http://127.0.0.1:47100/ui。
	UIAddr string
	// Restart 是「重启」菜单项的回调（nil 时该菜单项置灰）。
	// 由 cmd 层注入：重启意味着退出当前进程并重新拉起，属于应用生命周期，不该由托盘自己决定。
	Restart func()
	// ConfigDir 是「打开配置目录」的目标；为空时该项置灰。
	ConfigDir string
	// Version 显示在托盘提示（tooltip）里。
	Version string
	// OpenURL 用系统默认程序打开 http(s) 链接或本地目录，便于测试注入；
	// nil 时使用系统默认实现。
	OpenURL func(string) error
	// LANToggle / RelayToggle 切换拓扑角色。切换需要重建传输层，因此实现里
	// 会写配置并触发重启（不是热切换）；nil 时对应菜单项置灰。
	LANToggle   func(enabled bool) error
	RelayToggle func(enabled bool) error
}

const (
	// historySlots 是菜单里固定的历史条目槽位数，对应 spec §9 的「最近 10 条」。
	historySlots = 10
	// refreshInterval 是状态轮询周期：每 1s 取一次快照，只有内容变化才改菜单。
	refreshInterval = time.Second
	// slotTitleLimit 是历史条目在菜单里的单行截断长度（字符数）。
	slotTitleLimit = 40
)

// Run 显示托盘并阻塞，直到用户选择「退出」。
func Run(ctrl engine.Controller, opts Options) {
	if ctrl == nil {
		panic("tray: ctrl 不能为 nil")
	}
	if opts.OpenURL == nil {
		opts.OpenURL = openTarget
	}
	t := &tray{ctrl: ctrl, opts: opts}
	systray.Run(t.onReady, t.onExit)
}

type tray struct {
	ctrl engine.Controller
	opts Options

	// mu 保护槽位缓存与图标状态。持有期间不调用 ctrl（避免与回调互等）。
	mu         sync.Mutex
	lastTip    string
	lastState  iconState
	lastTitle  string
	lastStatus string
	hasIcon    bool
	slotIDs    [historySlots]string
	slotShown  [historySlots]bool

	title   *systray.MenuItem
	status  *systray.MenuItem
	slots   [historySlots]*systray.MenuItem
	openAll *systray.MenuItem
	push    *systray.MenuItem
	paused  *systray.MenuItem
	auto    *systray.MenuItem
	mdns    *systray.MenuItem
	config  *systray.MenuItem
	restart *systray.MenuItem
	lan     *systray.MenuItem
	relay   *systray.MenuItem
	quit    *systray.MenuItem
}

// onReady 在 systray 就绪后被调用（原生 UI 线程），此时才能建菜单、设图标。
func (t *tray) onReady() {
	t.buildMenu()
	t.refresh() // 先渲染一次，避免用户第一眼看到空壳菜单
	go t.loop()
}

func (t *tray) onExit() {
	log.Printf("tray: 托盘已退出")
}

// buildMenu 一次性创建全部菜单项。历史槽位的回调只捕获索引 i，
// 具体条目 ID 在刷新时写入槽位缓存，点击时再查。
func (t *tray) buildMenu() {
	t.title = systray.AddMenuItem("clipshare", "clipshare 局域网剪贴板共享")
	t.title.Disable()
	t.status = systray.AddMenuItem("正在读取状态…", "同步状态")
	t.status.Disable()
	systray.AddSeparator()

	for i := 0; i < historySlots; i++ {
		idx := i
		item := systray.AddMenuItem("", "历史条目")
		item.Hide()
		item.Click(func() { go t.pullSlot(idx) })
		t.slots[i] = item
	}

	systray.AddSeparator()
	t.openAll = systray.AddMenuItem("查看全部历史…", t.opts.UIAddr)
	t.openAll.Click(func() { go t.openURL(t.opts.UIAddr) })
	t.push = systray.AddMenuItem("发送当前剪贴板", "把当前剪贴板内容强制广播一次（S4）")
	t.push.Click(func() { go t.pushCurrent() })
	t.paused = systray.AddMenuItemCheckbox("暂停同步", "暂停收发远端条目；本地复制仍入历史（S5）", false)
	t.paused.Click(func() { go t.togglePaused() })
	t.auto = systray.AddMenuItemCheckbox("自动应用远端剪贴板", "收到远端条目时自动写入本机剪贴板（S2）", true)
	t.auto.Click(func() { go t.toggleAutoApply() })
	t.mdns = systray.AddMenuItemCheckbox("启用 mDNS 发现", "关闭后只用配置里的 seeds 连接（design §7）", true)
	t.mdns.Click(func() { go t.toggleMDNS() })

	// 拓扑开关：同一份配置同时支持「局域网 P2P」与「经中心点」，在这里一键切换。
	// 切换会重建传输层，所以实现是「写配置 + 重启」而不是热切换。
	t.lan = systray.AddMenuItemCheckbox("启用局域网对等",
		"同网段内与其它机器直连互推（关闭后只保留中继）", true)
	if t.opts.LANToggle == nil {
		t.lan.Disable()
		t.lan.SetTooltip("当前模式不支持切换")
	} else {
		t.lan.Click(func() { go t.toggleLAN() })
	}
	t.relay = systray.AddMenuItemCheckbox("启用中心点中继",
		"经中心点在不同网络之间同步（需要配置 relayURL）", false)
	if t.opts.RelayToggle == nil {
		t.relay.Disable()
		t.relay.SetTooltip("未配置 relayURL")
	} else {
		t.relay.Click(func() { go t.toggleRelay() })
	}

	systray.AddSeparator()
	t.config = systray.AddMenuItem("打开配置目录", t.opts.ConfigDir)
	if t.opts.ConfigDir == "" {
		t.config.Disable()
		t.config.SetTooltip("未设置配置目录")
	} else {
		t.config.Click(func() { go t.openConfigDir() })
	}
	t.restart = systray.AddMenuItem("重启（重新加载配置）", "停止后立即重新启动 clipshare")
	if t.opts.Restart == nil {
		t.restart.Disable()
	} else {
		t.restart.Click(func() { go t.doRestart() })
	}

	t.quit = systray.AddMenuItem("退出", "退出 clipshare")
	t.quit.Click(func() { systray.Quit() })

	// 记一条「菜单已就绪」的日志：托盘是纯 GUI，出问题时从日志看不出到底建成了什么。
	// 拓扑开关是后加的关键项，明确记下它们是否创建成功，便于远程排障。
	log.Printf("tray: 菜单已就绪（历史槽 %d 个；拓扑开关：局域网=%v 中继=%v）",
		historySlots, t.lan != nil, t.relay != nil)
}

// toggleLAN 切换局域网对等。写配置后由 cmd 层触发重启（传输层需要重建）。
func (t *tray) toggleLAN() {
	cur := t.currentSnapshot().LANEnabled
	if err := t.opts.LANToggle(!cur); err != nil {
		log.Printf("tray: 切换局域网对等失败: %v", err)
	}
	t.refresh()
}

// toggleRelay 切换中心点中继。
func (t *tray) toggleRelay() {
	snap := t.currentSnapshot()
	cur := snap.RelayURL != "" && snap.RelayEnabled
	if err := t.opts.RelayToggle(!cur); err != nil {
		log.Printf("tray: 切换中继失败: %v", err)
	}
	t.refresh()
}

func (t *tray) currentSnapshot() engine.Snapshot { return t.ctrl.Snapshot() }

// doRestart 请求重启并让托盘循环退出：退出走正常路径，engine 会被停掉、
// 历史会落盘，cmd 层随后用同一套参数重新拉起进程。
func (t *tray) doRestart() {
	t.opts.Restart()
	systray.Quit()
}

// Quit 让托盘循环退出。供 cmd 层在「收到重启请求」时调用——
// 那条路径不经过托盘菜单，但同样需要把阻塞在 systray.Run 里的主 goroutine 叫醒。
func Quit() { systray.Quit() }

// loop 每 refreshInterval 轮询一次；refresh 内部逐项 diff，
// 没变化就不碰任何菜单项（避免无谓的 D-Bus 通知与闪烁）。
func (t *tray) loop() {
	tk := time.NewTicker(refreshInterval)
	defer tk.Stop()
	for range tk.C {
		t.refresh()
	}
}

// refresh 取一次快照并同步到菜单。可能在两个 goroutine 里被调用，
// 故整段更新串行化。
func (t *tray) refresh() {
	snap := t.ctrl.Snapshot()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.syncTitle(snap)
	t.syncStatus(snap)
	t.syncSlots(snap.Recent)
	t.syncChecks(snap)
	t.syncIcon(snap)
}

func (t *tray) syncTitle(snap engine.Snapshot) {
	if want := titleText(snap); want != t.lastTitle {
		t.title.SetTitle(want)
		t.lastTitle = want
	}
	if tip := tooltipText(t.opts.Version, snap); tip != t.lastTip {
		systray.SetTooltip(tip)
		t.lastTip = tip
	}
}

func (t *tray) syncStatus(snap engine.Snapshot) {
	if want := statusText(snap); want != t.lastStatus {
		t.status.SetTitle(want)
		t.lastStatus = want
	}
}

// syncSlots 用存量槽位表达当前最近条目：有内容就改标题并显示，
// 没有内容就隐藏。绝不新增/删除菜单项。
func (t *tray) syncSlots(recent []model.Entry) {
	for i := 0; i < historySlots; i++ {
		item := t.slots[i]
		if i >= len(recent) {
			if t.slotShown[i] {
				item.Hide()
				t.slotShown[i] = false
			}
			t.slotIDs[i] = ""
			continue
		}
		e := recent[i]
		// ID 变了说明内容变了；槽位从隐藏转显示时也要写一次标题。
		if !t.slotShown[i] || t.slotIDs[i] != e.ID {
			item.SetTitle(slotTitle(e))
			item.SetTooltip(slotTooltip(e))
			t.slotIDs[i] = e.ID
		}
		if !t.slotShown[i] {
			item.Show()
			t.slotShown[i] = true
		}
	}
}

func (t *tray) syncChecks(snap engine.Snapshot) {
	syncCheck(t.paused, snap.Paused)
	syncCheck(t.auto, snap.AutoApply)
	syncCheck(t.mdns, snap.MDNS)
	if t.lan != nil {
		syncCheck(t.lan, snap.LANEnabled)
	}
	if t.relay != nil {
		syncCheck(t.relay, snap.RelayURL != "" && snap.RelayEnabled)
		if snap.RelayURL == "" {
			t.relay.SetTitle("启用中心点中继（未配置 relayURL）")
		} else if snap.RelayEnabled {
			// 标题里体现连通性：光有地址不代表连得上（指纹错、中心点挂了都会连不上）。
			if snap.RelayConnected {
				t.relay.SetTitle("启用中心点中继（已连通）")
			} else {
				t.relay.SetTitle("启用中心点中继（未连通）")
			}
		} else {
			t.relay.SetTitle("启用中心点中继")
		}
	}
}

func syncCheck(item *systray.MenuItem, want bool) {
	if item.Checked() == want {
		return
	}
	if want {
		item.Check()
		return
	}
	item.Uncheck()
}

func (t *tray) syncIcon(snap engine.Snapshot) {
	want := iconStateFor(snap)
	if t.hasIcon && want == t.lastState {
		return
	}
	systray.SetIcon(iconBytes(want))
	t.lastState = want
	t.hasIcon = true
}

func (t *tray) pullSlot(i int) {
	t.mu.Lock()
	id := t.slotIDs[i]
	t.mu.Unlock()
	if id == "" {
		return
	}
	if err := t.ctrl.PullEntry(id); err != nil {
		log.Printf("tray: 取回历史条目失败: %v", err)
	}
	t.refresh()
}

func (t *tray) pushCurrent() {
	if err := t.ctrl.PushCurrent(); err != nil {
		log.Printf("tray: 发送当前剪贴板失败: %v", err)
	}
	t.refresh()
}

func (t *tray) togglePaused() {
	t.ctrl.SetPaused(!t.paused.Checked())
	t.refresh() // 立刻回显真实状态，不等下一次轮询
}

func (t *tray) toggleAutoApply() {
	if err := t.ctrl.SetAutoApply(!t.auto.Checked()); err != nil {
		log.Printf("tray: 切换自动应用失败: %v", err)
	}
	t.refresh()
}

func (t *tray) toggleMDNS() {
	if err := t.ctrl.SetMDNS(!t.mdns.Checked()); err != nil {
		log.Printf("tray: 切换 mDNS 失败: %v", err)
	}
	t.refresh()
}

func (t *tray) openURL(target string) {
	if target == "" {
		log.Printf("tray: Web UI 地址为空，跳过")
		return
	}
	if err := t.opts.OpenURL(target); err != nil {
		log.Printf("tray: 打开 %s 失败: %v", target, err)
	}
}

func (t *tray) openConfigDir() {
	if t.opts.ConfigDir == "" {
		return
	}
	if err := t.opts.OpenURL(t.opts.ConfigDir); err != nil {
		log.Printf("tray: 打开配置目录失败: %v", err)
	}
}

// titleText 是禁用的标题行：clipshare · <nodeName> · <房间码前 6 位>。
// 节点名过长时截断，避免把菜单撑变形。
func titleText(snap engine.Snapshot) string {
	name := model.Truncate(snap.NodeName, 24)
	if snap.NodeName == "" {
		name = "clipshare"
	}
	if room := shortRoom(snap.RoomHash); room != "" {
		return "clipshare · " + name + " · " + room
	}
	return "clipshare · " + name
}

func shortRoom(hash string) string {
	if len(hash) <= 6 {
		return hash
	}
	return hash[:6]
}

// statusText 是禁用的状态行，三态与 spec §9 一致（暂停优先于无节点）。
func statusText(snap engine.Snapshot) string {
	switch {
	case snap.Paused:
		return "已暂停"
	case snap.Peers == 0:
		return "无可用节点"
	default:
		return fmt.Sprintf("与 %d 台同步中", snap.Peers)
	}
}

// slotTitle 是历史槽位标题：来源标签 + 单行截断内容。
func slotTitle(e model.Entry) string {
	label := "本机"
	if e.Origin != "" {
		label = e.OriginName
		if label == "" {
			label = "远端"
		}
	}
	return "[" + label + "] " + model.Truncate(e.Content, slotTitleLimit)
}

func slotTooltip(e model.Entry) string {
	return model.Truncate(e.Content, 120)
}

// tooltipText 是托盘图标的悬停提示：版本、状态，以及最近一次可展示的错误。
func tooltipText(version string, snap engine.Snapshot) string {
	tip := "clipshare"
	if version != "" {
		tip += " " + version
	}
	tip += " · " + statusText(snap)
	if snap.LastError != "" {
		tip += " · " + model.Truncate(snap.LastError, 80)
	}
	return tip
}
