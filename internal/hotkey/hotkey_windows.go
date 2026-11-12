//go:build windows

// Package hotkey 注册「弹出剪贴板历史」的全局热键（Ctrl+Alt+V）。
//
// # 为什么整件事被绑在一个线程上
//
// Win32 里窗口、热键、消息队列都是**线程**资源而不是进程资源：
// RegisterHotKey(hWnd=0) 把 WM_HOTKEY 投递到注册热键的那个线程的消息队列，
// 而窗口只能由创建它的线程处理消息。所以本包做三件事在同一个 OS 线程上：
// 注册热键、跑 GetMessageW 循环、**同步**调用 onPress。
//
// 同步调用 onPress 是硬性要求而不是风格问题：onPress 里要调 picker.Run
// 创建选择窗口，那个窗口的消息循环（picker 内部的 GetMessageW/DispatchMessageW）
// 必须在同一个线程上跑，否则会得到一个「画出来了但不响应键盘」的幽灵窗口。
//
// # 用法
//
// 调用方必须在**独立 goroutine** 里调 Start（它会阻塞到 Stop 或致命错误），
// 主 goroutine 继续跑托盘之类的事情：
//
//	go func() {
//		if err := hotkey.Start(onPress); err != nil {
//			log.Printf("全局热键不可用: %v", err)
//		}
//	}()
//	defer hotkey.Stop()
//
// Start 重复调用会返回错误（不会 panic）；Stop 在没有运行时会静默返回。
// 在 onPress 内部调 Stop 也支持（识别出「自己等自己」并跳过等待）。
package hotkey

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ErrUnsupported 表示当前平台不支持全局热键。
//
// Windows 上 Start 永远不返回它（这里有 RegisterHotKey），但调用方可以统一用
// errors.Is(err, hotkey.ErrUnsupported) 判断「这个平台根本没有这个能力」，
// 而不是把错误当成故障上报。
//
// 注意：本变量在 hotkey_windows.go 与 hotkey_other.go 里各声明一份，
// 靠构建标签二选一编译；改动时两处必须保持一致。
var ErrUnsupported = errors.New("当前平台不支持全局热键")

const (
	// hotkeyID 是本进程注册的热键编号：WM_HOTKEY 用它区分是哪个热键。
	// 本包只注册一个热键，所以固定 1。
	hotkeyID = 1

	// hotkeyVK 是热键的主键 'V'（0x56）。
	hotkeyVK = 0x56

	// RegisterHotKey 的修饰键位。
	//
	// MOD_NOREPEAT 是必需的而不是可选项：按住 Ctrl+Alt+V 不放时，键盘自动重复
	// 会让系统不停投递 WM_HOTKEY，选择窗口关掉一个又弹一个（而且关完还要
	// 把积压的消息一条条处理完）。加上它，一次按下只投递一条。
	modAlt      = 0x0001
	modControl  = 0x0002
	modNoRepeat = 0x4000
	wmHotkey    = 0x0312
	wmQuit      = 0x0012
	pmNoRemove  = 0x0000

	// stopTimeout 是 Stop 等待热键线程收尾的上限。
	//
	// 选择窗口开着时，热键线程正阻塞在窗口自己的消息循环里，Stop 得等它关掉。
	// 超过这个时长就放弃等待（热键会随进程退出由系统回收），绝不把调用方卡死。
	stopTimeout = 2 * time.Second
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterHotKey     = user32.NewProc("RegisterHotKey")
	procUnregisterHotKey   = user32.NewProc("UnregisterHotKey")
	procGetMessageW        = user32.NewProc("GetMessageW")
	procPeekMessageW       = user32.NewProc("PeekMessageW")
	procTranslateMessage   = user32.NewProc("TranslateMessage")
	procDispatchMessageW   = user32.NewProc("DispatchMessageW")
	procPostThreadMessageW = user32.NewProc("PostThreadMessageW")
	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")
)

// winMsg 对应 Win32 的 MSG。
//
// 字段顺序必须与 winuser.h 完全一致：GetMessageW 按它自己的 sizeof(MSG)
// 往这块内存里写，顺序错了就是读到垃圾。句柄/指针用 uintptr，32/64 位下
// 编译器按自然对齐补齐后的布局与 C 相同（amd64 48 字节，386 28 字节）。
type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

// 运行状态。全部由 mu 保护：Start 在热键线程上写，Stop 可能从任何 goroutine 调。
var (
	mu      sync.Mutex
	running bool
	tid     uint32        // 热键线程的 OS 线程 id；0 表示没有在运行
	exitCh  chan struct{} // 关闭表示消息循环已退出、热键已注销
)

// Start 注册全局热键 Ctrl+Alt+V，每次按下调用 onPress；
// 阻塞直到 Stop 或致命错误。调用方应在独立 goroutine 中调用它。
//
// onPress 在热键线程上**同步**执行（见包注释），所以它里面可以直接调
// picker.Run 创建选择窗口；也正因为如此，onPress 跑多久，热键就多久不响应。
func Start(onPress func()) error {
	// 整个函数生命周期都绑在这一个 OS 线程上：注册热键、跑消息循环、
	// 调 onPress（里面会创建窗口）必须是同一个线程，这是 Win32 的线程亲和性。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mu.Lock()
	if running {
		mu.Unlock()
		// 重复 Start 是调用方的编程错误，但不能 panic（也不能悄悄起第二个线程
		// 去抢注册同一个热键）。
		return fmt.Errorf("hotkey: 全局热键已在运行（重复 Start）")
	}
	running = true
	tid = currentThreadID()
	exit := make(chan struct{})
	exitCh = exit
	mu.Unlock()

	// 无论从哪条路径退出，都要复位状态，否则 Stop 之后再也 Start 不起来。
	defer func() {
		mu.Lock()
		running = false
		tid = 0
		exitCh = nil
		mu.Unlock()
		close(exit)
	}()

	if err := register(); err != nil {
		return err
	}
	// 注销必须与注册在同一线程上做，所以放在消息循环之后的退出路径上。
	defer unregister()

	// 先强制建出本线程的消息队列：PostThreadMessageW 对「还没有消息队列」的
	// 线程会失败，Stop 在那之前投递的 WM_QUIT 就会被丢掉。PM_NOREMOVE 只建队列、
	// 不取消息。
	var probe winMsg
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&probe)), 0, 0, 0, pmNoRemove)
	runtime.KeepAlive(&probe)

	var msg winMsg
	for {
		ret, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			return fmt.Errorf("hotkey: 消息循环失败: %v", callErr)
		case 0:
			// WM_QUIT：Stop 让我们退出，正常收尾。
			return nil
		}
		if msg.message == wmHotkey && msg.wParam == hotkeyID {
			callOnPress(onPress)
			continue
		}
		// 本包自己没有窗口，正常收不到别的消息；万一将来有，也走标准分发，
		// 免得不分发的消息把队列堵住。
		if msg.hwnd != 0 {
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
		}
	}
}

// Stop 注销热键并结束 Start 的阻塞。
//
// 没有 Start 就调 Stop 是静默无操作（不 panic）；重复 Stop 同样安全。
// 若此时选择窗口正开着，Stop 会等它关掉（最多 stopTimeout）。
func Stop() {
	mu.Lock()
	if !running {
		mu.Unlock()
		return
	}
	thread := tid
	exit := exitCh
	// onPress 里调 Stop 时，我们自己就跑在热键线程上：等自己退出会死锁。
	self := currentThreadID() == thread
	mu.Unlock()

	if thread == 0 || exit == nil {
		return
	}

	// 用 PostThreadMessageW 投 WM_QUIT，而不是关窗口之类的手段：本线程没有
	// 自己的窗口，投递到线程队列是唯一不依赖窗口句柄的退出方式。
	procPostThreadMessageW.Call(uintptr(thread), wmQuit, 0, 0)

	if self {
		// 当前就在热键线程上：消息循环会在本函数返回后读到 WM_QUIT 并退出，
		// UnregisterHotKey 由它在那条路径上完成。
		return
	}
	select {
	case <-exit:
	case <-time.After(stopTimeout):
		log.Printf("hotkey: 等待热键线程退出超时（%v），放弃等待", stopTimeout)
	}
}

// callOnPress 在热键线程上同步执行回调。
//
// 同步是硬性要求：onPress 里的 picker.Run 要创建窗口并跑它自己的消息循环，
// 而窗口只能在创建它的线程上处理消息。丢到别的 goroutine 里跑会得到
// 「窗口不响应键盘」的幽灵窗口。
func callOnPress(onPress func()) {
	if onPress == nil {
		return
	}
	defer func() {
		// 选择窗口是自绘 + 手写 syscall 的代码，万一 panic，让整个进程（含托盘）
		// 陪葬太粗暴；记录后继续跑消息循环，至少热键还能用。
		if r := recover(); r != nil {
			log.Printf("hotkey: 热键回调 panic（已恢复，热键仍可用）: %v", r)
		}
	}()
	onPress()
}

// register 在**当前线程**注册 Ctrl+Alt+V。
func register() error {
	ok, _, callErr := procRegisterHotKey.Call(
		0, // hWnd=0：WM_HOTKEY 投递到本线程的消息队列
		hotkeyID,
		modControl|modAlt|modNoRepeat,
		hotkeyVK,
	)
	if ok == 0 {
		// 最常见的原因：别的程序已经占了这个组合（输入法、截图/录屏工具、
		// 显卡驱动面板都会抢 Ctrl+Alt+V）。
		return fmt.Errorf("hotkey: 注册 Ctrl+Alt+V 失败（可能已被其它程序占用）: %v", callErr)
	}
	return nil
}

// unregister 在**当前线程**注销热键（Win32 要求与 RegisterHotKey 同线程）。
func unregister() {
	procUnregisterHotKey.Call(0, hotkeyID)
}

// currentThreadID 返回当前 goroutine 所在 OS 线程的 id。
//
// 注意它的语义：goroutine 会在 OS 线程之间迁移，所以这个值只有在
// runtime.LockOSThread 之后才有稳定的意义（Start 与 onPress 都在锁住的线程上）。
func currentThreadID() uint32 {
	id, _, _ := procGetCurrentThreadID.Call()
	return uint32(id)
}
