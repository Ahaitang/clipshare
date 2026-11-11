package clip

import (
	stdsync "sync"
	"sync/atomic"
	"time"

	"clipshare/internal/model"
)

// DefaultInterval 是默认轮询间隔，与 config.DefaultPollIntervalMs 一致。
const DefaultInterval = 400 * time.Millisecond

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
		clip:     c,
		interval: interval,
		onChange: onChange,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start 启动后台轮询。重复调用无效。
//
// 启动时先读一次建立基线：进程启动前就已经在剪贴板里的内容不算「用户刚复制」，
// 不会触发启动广播。轮询读到错误时跳过本轮，保留基线。
func (p *Poller) Start() {
	p.startOnce.Do(func() {
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

	p.mu.Lock()
	if !p.primed {
		p.primed = true
		p.lastSeen = content
		p.mu.Unlock()
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
