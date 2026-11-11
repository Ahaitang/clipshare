package clip

import (
	stdsync "sync"
	"sync/atomic"
	"time"

	"clipshare/internal/model"
)

// DefaultInterval 是默认轮询间隔，与 config.DefaultPollIntervalMs 一致。
const DefaultInterval = 400 * time.Millisecond

// defaultPrimeWindow 是启动阶段重试建立基线的窗口上限。
const defaultPrimeWindow = 2 * time.Second

// Poller 轮询剪贴板，发现内容变化时回调。
//
// 变更检测只用「上次读到的内容」（lastSeen），不使用 suppress 标志位：
// 标志位是「一次性消费」的，如果程序写入后标志没被消费（例如轮询正好
// 在两次写入之间错过），用户随后复制同样内容就会被误吞。直接改写
// lastSeen 没有这个坑——程序写入后 lastSeen 已经是该内容，下一次轮询
// 读到相同内容自然不算变化。
//
// Poller 只负责「变没变」；内容为空（图片）或超限由调用方（engine）判断，
// 因此复制图片时回调会照常收到空字符串。
type Poller struct {
	clip     Clipboard
	interval time.Duration
	onChange func(string)

	mu       stdsync.Mutex
	lastSeen string
	primed   bool // 是否已经用首次读到的内容建立基线

	// primeWindow 是启动阶段「读不到就在这个窗口内重试建基线」的时长。
	// 窗口过后仍没读到内容时，下一次成功读取按「变化」处理（见 pollOnce）。
	// 单独放字段是为了让测试能缩短它。
	primeWindow   time.Duration
	primeDeadline time.Time

	startOnce stdsync.Once
	stopOnce  stdsync.Once
	running   atomic.Bool

	stop chan struct{}
	done chan struct{}
}

// NewPoller 创建轮询器。interval <= 0 时使用 DefaultInterval；
// onChange 为 nil 时只维护基线、不回调。
func NewPoller(c Clipboard, interval time.Duration, onChange func(string)) *Poller {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Poller{
		clip:        c,
		interval:    interval,
		onChange:    onChange,
		primeWindow: defaultPrimeWindow,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
}

// Start 启动后台轮询。重复调用无效。
//
// 启动时先读一次建立基线：进程启动前就已经在剪贴板里的内容不算「用户刚复制」，
// 不会触发启动广播。轮询读到错误时跳过本轮，保留基线。
func (p *Poller) Start() {
	p.startOnce.Do(func() {
		p.mu.Lock()
		if p.primeWindow <= 0 {
			p.primeWindow = defaultPrimeWindow
		}
		p.primeDeadline = time.Now().Add(p.primeWindow)
		p.mu.Unlock()

		p.running.Store(true)
		go p.loop()
	})
}

// Stop 停止轮询并等待后台 goroutine 退出。
//
// 重复调用安全；也可在 Start 之前调用（此时直接返回）。
func (p *Poller) Stop() {
	p.stopOnce.Do(func() { close(p.stop) })
	if p.running.Load() {
		<-p.done
	}
}

// MarkSeen 记录「这份内容已经是最新状态」，用于程序自己写入剪贴板之后。
//
// 直接改写 lastSeen（而不是置一个 suppress 标志），后续轮询读到同样内容
// 就不会触发 OnChange；用户之后再复制别的内容仍会正常触发。
func (p *Poller) MarkSeen(content string) {
	content = model.Normalize(content)
	p.mu.Lock()
	p.lastSeen = content
	p.primed = true
	p.mu.Unlock()
}

// loop 先同步读一次建立基线，然后按 interval 轮询。
func (p *Poller) loop() {
	defer close(p.done)
	p.pollOnce()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.pollOnce()
		}
	}
}

// pollOnce 读一次剪贴板并比较基线。回调在锁外执行，
// 这样回调里调用 MarkSeen 不会死锁（engine 收到远端内容时会这么做）。
func (p *Poller) pollOnce() {
	content, err := p.clip.Read()
	if err != nil {
		// 读失败（命令临时不可用、剪贴板被占用）视为本轮无变化，保留基线。
		return
	}
	content = model.Normalize(content)

	now := time.Now()
	p.mu.Lock()
	if !p.primed {
		p.primed = true
		p.lastSeen = content
		onChange := p.onChange
		expired := !p.primeDeadline.IsZero() && now.After(p.primeDeadline)
		p.mu.Unlock()
		if !expired {
			// 窗口内读到内容：这是进程启动前就躺在剪贴板里的东西，算基线，不回调
			// （否则每次启动都会把旧内容广播一遍）。
			return
		}
		// 窗口已过却一直没能建立基线（启动阶段读全失败：Windows 空剪贴板、
		// 命令临时不可用……）。这时第一次成功读取很可能就是用户启动后的第一次复制，
		// 必须按「变化」处理。
		//
		// 取舍：宁可多发一条用户自己的剪贴板内容（对端按内容去重，且内容本来就是
		// 用户自己复制的），也不能静默吞掉用户的第一次复制——那种情况下用户完全
		// 没有反馈，只会觉得「同步坏了」。
		if content == "" || onChange == nil {
			return
		}
		onChange(content)
		return
	}
	changed := content != p.lastSeen
	if changed {
		p.lastSeen = content
	}
	onChange := p.onChange
	p.mu.Unlock()

	if changed && onChange != nil {
		onChange(content)
	}
}
