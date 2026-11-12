//go:build windows

package picker

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

// show 在 Windows 上展示选择窗口（Win32 原生窗口，文本渲染交给 GDI，CJK 无额外依赖）。
//
// 实现要点：
//   - 无边框（WS_POPUP）、置顶（WS_EX_TOPMOST）、640×430、屏幕居中；
//   - 全部自绘（不用 ListBox/Edit 等标准控件）：WM_PAINT 里 BeginPaint +
//     SetBkMode(TRANSPARENT) + TextOutW/DrawTextW；选中行先 FillRect 高亮；
//   - 键盘全部在窗口的 WM_KEYDOWN / WM_CHAR 里处理，按键语义交给
//     show_state.go 的纯逻辑（各平台共用，可在 Linux 上单测）；
//   - 鼠标：WM_LBUTTONDOWN 命中的行直接确认，WM_MOUSEWHEEL 滚动；
//   - 退出：确认/取消后 DestroyWindow，消息循环因 WM_DESTROY → WM_QUIT 结束。
//
// **线程约束（重要）**：Win32 的窗口只属于创建它的线程，消息也只投递给那个线程。
// 本函数必须在将要跑消息循环的那个线程上调用，且该线程必须被
// runtime.LockOSThread 锁住——internal/hotkey 的 Start 已经这样做了。
// 在普通 goroutine 里调用会得到「窗口画出来了但不响应键盘」的幽灵窗口。
func show(items []Item, opts Options) (Result, error) {
	// 防御性重申一次：即使调用方忘了锁线程，本次调用期间也不会被迁移到别的线程。
	// （嵌套 LockOSThread 是安全的：计数配对 Unlock 即可。）
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w := &winWindow{
		ui:    newUIState(items),
		title: uiTitle(opts),
	}

	// 先抢占「当前窗口」这个唯一名额：进程内同一时刻只允许一个选择窗口。
	// 热键线程被 picker.Run 阻塞住时天然只有一个；但托盘菜单可能在别的
	// goroutine 里再调一次 picker.Run，那种情况下第二个窗口直接拒绝，
	// 而不是让两个窗口互相覆盖包级状态。
	if err := claimWindow(w); err != nil {
		return Result{}, err
	}
	defer releaseWindow(w)

	// GDI 对象与窗口句柄的释放都放这里统一兜底：包括「建到一半失败」的路径，
	// 以及万一 panic 展开（defer 照样会跑）。
	defer w.release()
	if err := w.createGDIObjects(); err != nil {
		return Result{}, err
	}
	if err := w.createWindow(); err != nil {
		return Result{}, err
	}

	w.reveal()
	if err := w.runLoop(); err != nil {
		return Result{}, err
	}
	return w.result, nil
}

// 窗口样式与消息常量（winuser.h）。
const (
	wsPopup        = 0x80000000 // 无边框弹出窗口
	wsExTopmost    = 0x00000008 // 置顶
	wsExToolWindow = 0x00000080 // 不出现在任务栏/Alt-Tab 里（选择窗口不该抢位置）
	swShow         = 5          // SW_SHOW

	smCXScreen = 0 // SM_CXSCREEN
	smCYScreen = 1 // SM_CYSCREEN

	wmDestroy     = 0x0002
	wmPaint       = 0x000F
	wmClose       = 0x0010
	wmEraseBkgnd  = 0x0014
	wmKeyDown     = 0x0100
	wmChar        = 0x0102
	wmLButtonDown = 0x0201
	wmMouseWheel  = 0x020A

	idcArrow = 32512 // IDC_ARROW：MAKEINTRESOURCE(32512)

	// 虚拟键码（WM_KEYDOWN 的 wParam）。
	vkBack   = 0x08
	vkReturn = 0x0D
	vkEscape = 0x1B
	vkPrior  = 0x21 // PageUp
	vkNext   = 0x22 // PageDown
	vkEnd    = 0x23
	vkHome   = 0x24
	vkUp     = 0x26
	vkDown   = 0x28

	// 滚轮：一格 120，正数表示往远离用户的方向滚（内容上移）。
	wheelDelta = 120
)

// GDI 常量。
const (
	fwNormal             = 400 // FW_NORMAL
	defaultCharset       = 1   // DEFAULT_CHARSET：按系统 ANSI 代码页挑 CJK 字形
	outDefaultPrecision  = 0   // OUT_DEFAULT_PRECIS
	clipDefaultPrecision = 0   // CLIP_DEFAULT_PRECIS
	cleartypeQuality     = 5   // CLEARTYPE_QUALITY
	pitchAndFamily       = 0   // DEFAULT_PITCH | FF_DONTCARE
	stockDefaultGUIFont  = 17  // DEFAULT_GUI_FONT（GetStockObject 的序号）
	winTransparent       = 1   // TRANSPARENT（SetBkMode）

	// DrawTextW 的格式位。
	dtLeft        = 0x0000
	dtCenter      = 0x0001
	dtRight       = 0x0002
	dtVCenter     = 0x0004 // 必须与 DT_SINGLELINE 一起用
	dtSingleLine  = 0x0020
	dtNoPrefix    = 0x0800 // 别把内容里的 '&' 吃掉（剪贴板内容常含 &）
	dtEndEllipsis = 0x8000
)

// 配色。COLORREF 是 0x00BBGGRR（注意红蓝分量是反的）。
var (
	colBg      = winRGB(250, 250, 250)
	colText    = winRGB(28, 28, 28)
	colDim     = winRGB(120, 120, 120)
	colSelBg   = winRGB(0, 120, 215)
	colSelText = winRGB(255, 255, 255)
	colLine    = winRGB(214, 214, 214)
)

// winRGB 把 RGB 分量拼成 COLORREF（0x00BBGGRR）。
func winRGB(r, g, b byte) uintptr {
	return uintptr(b)<<16 | uintptr(g)<<8 | uintptr(r)
}

// winRect 对应 Win32 的 RECT。
type winRect struct{ left, top, right, bottom int32 }

// winSize 对应 Win32 的 SIZE（GetTextExtentPoint32W 的输出）。
type winSize struct{ cx, cy int32 }

// winMsg 对应 Win32 的 MSG。
//
// 字段顺序必须与 winuser.h 一致：GetMessageW 按它自己的 sizeof(MSG) 往这块
// 内存里写，顺序错了就是读到垃圾。句柄/指针用 uintptr，32/64 位下编译器按
// 自然对齐补齐后的布局与 C 相同（amd64 48 字节，386 28 字节）。
type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

// winWndClassEx 对应 WNDCLASSEXW。cbSize 由 unsafe.Sizeof 填，不用手算。
type winWndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// winPaintStruct 对应 PAINTSTRUCT。
type winPaintStruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     winRect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetFocus            = user32.NewProc("SetFocus")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProc = user32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput   = user32.NewProc("AttachThreadInput")
	procBringWindowToTop    = user32.NewProc("BringWindowToTop")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procInvalidateRect      = user32.NewProc("InvalidateRect")
	procBeginPaint          = user32.NewProc("BeginPaint")
	procEndPaint            = user32.NewProc("EndPaint")
	procGetClientRect       = user32.NewProc("GetClientRect")
	procFillRect            = user32.NewProc("FillRect")
	procFrameRect           = user32.NewProc("FrameRect")
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procGetDC               = user32.NewProc("GetDC")
	procReleaseDC           = user32.NewProc("ReleaseDC")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procMessageBeep         = user32.NewProc("MessageBeep")

	procGetModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadID = kernel32.NewProc("GetCurrentThreadId")

	procCreateFontW      = gdi32.NewProc("CreateFontW")
	procCreateSolidBrush = gdi32.NewProc("CreateSolidBrush")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procGetStockObject   = gdi32.NewProc("GetStockObject")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procTextOutW         = gdi32.NewProc("TextOutW")
	// DrawTextW 在 user32 里，不是 gdi32 —— 放错 DLL 会在运行时 panic
	// （Failed to find DrawTextW procedure in gdi32.dll），编译期完全看不出来。
	procDrawTextW            = user32.NewProc("DrawTextW")
	procGetTextExtentPoint32 = gdi32.NewProc("GetTextExtentPoint32W")
	procGetTextFaceW         = gdi32.NewProc("GetTextFaceW")
)

const (
	winClassName = "ClipsharePickerWindow"

	// winFontHeight 是正文的逻辑字号（像素）。负值表示「字符高度」而不是
	// 「单元格高度」，这样中英文混排基线一致。
	winFontHeight = -15

	// winMaxChromeWidth 是行尾次要信息（来源 · 时间）最多占的横向空间；
	// 来源名很长时宁可让它自己截断，也别把内容挤没。
	winMaxChromeWidth = 160
)

var (
	// winProcCallback 保存 syscall.NewCallback 的结果：Go 的 callback 一旦被
	// GC 回收，Win32 再回调过来就是野指针，所以必须挂在包级变量上。
	winProcCallback = syscall.NewCallback(winProc)

	// winClassOnce 保证窗口类只注册一次。类不随窗口销毁而消失（我们不调
	// UnregisterClassW），第二次弹窗直接复用同一个 wndproc。
	winClassOnce sync.Once
	winClassErr  error

	// activeWindow 是当前正在显示的选择窗口。
	//
	// 为什么用包级变量而不是窗口的 USERDATA 槽：写 USERDATA 要用
	// SetWindowLongPtrW，而它在 32 位 Windows 上没有独立导出（头文件里只是个
	// 宏，实际只有 SetWindowLongW），纯 syscall 方案在 386 上会漏名字。
	// 包级变量没有这个问题；winMu 保证「抢占/释放」是原子的。
	winMu        sync.Mutex
	activeWindow *winWindow
)

// winWindow 是一个选择窗口的全部状态。
//
// 它的字段只在创建窗口的那个线程上被读写（窗口过程与消息循环都是线程亲和的），
// 因此内容本身不需要加锁；只有 activeWindow 这个「归属」用 winMu 保护。
type winWindow struct {
	hwnd      uintptr
	ui        *uiState
	title     string
	result    Result
	closed    bool
	wheelAcc  int // 滚轮累积量：高精度滚轮/触控板一次可能只给 30、60
	surrogate uiSurrogate
	font      uintptr
	fontOwned bool // font 是我们创建的（可 DeleteObject），不是 stock object
	brBg      uintptr
	brSel     uintptr
	brLine    uintptr
}

// claimWindow 抢占「当前窗口」名额；已有窗口在显示时返回错误。
func claimWindow(w *winWindow) error {
	winMu.Lock()
	defer winMu.Unlock()
	if activeWindow != nil {
		return errors.New("picker: 已经有一个选择窗口在显示")
	}
	activeWindow = w
	return nil
}

// releaseWindow 归还名额（只在还是自己的时候归还，避免误清掉别人的）。
func releaseWindow(w *winWindow) {
	winMu.Lock()
	defer winMu.Unlock()
	if activeWindow == w {
		activeWindow = nil
	}
}

// currentWindow 返回当前窗口（窗口过程里用）。
func currentWindow() *winWindow {
	winMu.Lock()
	defer winMu.Unlock()
	return activeWindow
}

// createGDIObjects 一次性建好绘制用的画刷与字体。
//
// 每条 GDI 对象整个窗口生命周期只建一次，避免在 WM_PAINT 里反复建/删——
// 那是 GDI 句柄泄漏与闪烁的老来源。
func (w *winWindow) createGDIObjects() error {
	w.brBg = createSolidBrush(colBg)
	w.brSel = createSolidBrush(colSelBg)
	w.brLine = createSolidBrush(colLine)
	if w.brBg == 0 || w.brSel == 0 || w.brLine == 0 {
		return errors.New("picker: 创建画刷失败（GDI 资源不可用）")
	}
	w.font, w.fontOwned = createUIFont(winFontHeight)
	return nil
}

// createWindow 注册窗口类并创建无边框置顶窗口，定位到屏幕中央。
func (w *winWindow) createWindow() error {
	// hInstance 用本 exe 的模块句柄：GetModuleHandleW(NULL) 返回的正是它。
	hinst, _, _ := procGetModuleHandleW.Call(0)
	if hinst == 0 {
		return errors.New("picker: GetModuleHandleW 失败")
	}
	if err := ensureWindowClass(hinst); err != nil {
		return err
	}

	className, err := syscall.UTF16PtrFromString(winClassName)
	if err != nil {
		return err
	}
	titlePtr, err := syscall.UTF16PtrFromString(w.title)
	if err != nil {
		return err
	}

	// 屏幕居中。多显示器时这里用的是主显示器；选择窗口由热键弹出，
	// 主屏是绝大多数情况下的正确选择。
	sw, _, _ := procGetSystemMetrics.Call(smCXScreen)
	sh, _, _ := procGetSystemMetrics.Call(smCYScreen)
	x := (int(sw) - uiWidth) / 2
	y := (int(sh) - uiHeight) / 2

	hwnd, _, callErr := procCreateWindowExW.Call(
		wsExTopmost|wsExToolWindow,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(titlePtr)),
		wsPopup,
		uintptr(x), uintptr(y), uintptr(uiWidth), uintptr(uiHeight),
		0, // 无父窗口
		0, // 无菜单
		hinst,
		0, // 不用 lpParam：窗口状态走包级 activeWindow（见注释）
	)
	runtime.KeepAlive(className)
	runtime.KeepAlive(titlePtr)
	if hwnd == 0 {
		return fmt.Errorf("picker: 创建选择窗口失败: %v", callErr)
	}
	w.hwnd = hwnd
	return nil
}

// ensureWindowClass 注册窗口类（进程内只做一次）。
func ensureWindowClass(hinst uintptr) error {
	winClassOnce.Do(func() {
		className, err := syscall.UTF16PtrFromString(winClassName)
		if err != nil {
			winClassErr = err
			return
		}
		cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
		wc := winWndClassEx{
			cbSize:        uint32(unsafe.Sizeof(winWndClassEx{})),
			style:         0,
			lpfnWndProc:   winProcCallback,
			hInstance:     hinst,
			hCursor:       cursor,
			hbrBackground: 0, // 背景由 WM_PAINT 自己填；用类画刷会多一次重绘/闪烁
			lpszClassName: className,
		}
		atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		runtime.KeepAlive(&wc)
		runtime.KeepAlive(className)
		if atom == 0 {
			winClassErr = fmt.Errorf("picker: RegisterClassExW 失败: %v", callErr)
		}
	})
	return winClassErr
}

// reveal 显示窗口并保证它能拿到键盘输入。
func (w *winWindow) reveal() {
	procShowWindow.Call(w.hwnd, swShow)
	procInvalidateRect.Call(w.hwnd, 0, 0)

	// SetForegroundWindow 只是「请求」：当前前台窗口属于别的进程时，
	// 热键弹出的进程默认没有夺前台的资格，请求会被系统忽略。所以必须走
	// AttachThreadInput 的兜底（见 forceForeground），否则窗口显示出来了
	// 但键盘输入进不来——这是这类热键弹窗最典型的失败形态。
	procSetForegroundWindow.Call(w.hwnd)
	w.forceForeground()
	procSetFocus.Call(w.hwnd)
}

// forceForeground 把本线程临时挂到当前前台窗口的输入队列上，从而获得
// 设置前台的资格。全部失败也不致命：窗口照样可见，用户点一下就恢复输入。
func (w *winWindow) forceForeground() {
	fore, _, _ := procGetForegroundWindow.Call()
	if fore == 0 || fore == w.hwnd {
		return
	}
	foreThread, _, _ := procGetWindowThreadProc.Call(fore, 0)
	self, _, _ := procGetCurrentThreadID.Call()
	if foreThread == 0 || foreThread == self {
		return
	}
	procAttachThreadInput.Call(self, foreThread, 1)
	defer procAttachThreadInput.Call(self, foreThread, 0)
	procSetForegroundWindow.Call(w.hwnd)
	procBringWindowToTop.Call(w.hwnd)
}

// runLoop 跑消息循环，直到窗口关闭（WM_DESTROY → PostQuitMessage → WM_QUIT）。
//
// 注意这个循环里也会收到本线程的其它消息（例如热键线程的 WM_HOTKEY）：
// DispatchMessageW 对 hwnd==0 的消息不做任何事，所以「选择窗口开着时再按一次
// 热键」会被安静地丢弃——这正好是想要的行为（不会叠出第二个窗口）。
func (w *winWindow) runLoop() error {
	var msg winMsg
	for {
		ret, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		switch int32(ret) {
		case -1:
			return fmt.Errorf("picker: 消息循环失败: %v", callErr)
		case 0:
			// WM_QUIT。正常路径下它是 DestroyWindow → WM_DESTROY → PostQuitMessage
			// 引发的（此时 w.closed 已置位）。若窗口还在，说明是外部（例如
			// hotkey.Stop）要本线程退出：先把窗口收掉（它的 WM_DESTROY 会把
			// WM_QUIT 重新投递一次），让外层热键循环也能看到并干净退出。
			if !w.closed {
				w.closed = true
				w.result = Result{}
				procDestroyWindow.Call(w.hwnd)
			}
			return nil
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// release 销毁窗口并释放 GDI 对象；可重复调用（出错提前返回也会走到这里）。
func (w *winWindow) release() {
	if w.hwnd != 0 && !w.closed {
		// 只在还没销毁时销毁：重复对已失效的句柄调 DestroyWindow 虽然通常
		// 只是返回 0，但句柄值万一被系统回收给别人就麻烦了。
		w.closed = true
		procDestroyWindow.Call(w.hwnd)
	}
	// stock object（DEFAULT_GUI_FONT）不能删，删了会影响整个进程的默认字体。
	if w.font != 0 && w.fontOwned {
		procDeleteObject.Call(w.font)
	}
	for _, br := range []uintptr{w.brBg, w.brSel, w.brLine} {
		if br != 0 {
			procDeleteObject.Call(br)
		}
	}
}

// winProc 是选择窗口的窗口过程。
//
// 它只在创建窗口的那个线程上被调用（消息循环在 show 所在的线程上跑），
// 所以对 winWindow 字段的读写不需要加锁。
func winProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	w := currentWindow()
	switch uint32(msg) {
	case wmPaint:
		if w != nil {
			w.paint(hwnd)
		} else {
			// 没有窗口状态也必须把绘制区域「验证」掉，否则系统会不停地
			// 重发 WM_PAINT（空转烧 CPU）。
			validatePaint(hwnd)
		}
		return 0
	case wmEraseBkgnd:
		// 背景在 WM_PAINT 里自己填，返回 1 表示「已擦除」，省一次闪烁。
		return 1
	case wmKeyDown:
		if w != nil {
			w.onKeyDown(uint32(wparam))
		}
		return 0
	case wmChar:
		if w != nil {
			w.onChar(uint16(wparam))
		}
		return 0
	case wmLButtonDown:
		if w != nil {
			w.onClick(lparam)
		}
		return 0
	case wmMouseWheel:
		if w != nil {
			w.onWheel(wparam)
		}
		return 0
	case wmClose:
		if w != nil {
			w.close()
		} else {
			procDestroyWindow.Call(hwnd)
		}
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)
	return r
}

// validatePaint 在拿不到窗口状态时也把绘制区域验证掉。
func validatePaint(hwnd uintptr) {
	var ps winPaintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc != 0 {
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	}
	// ps 的生命周期必须覆盖到 EndPaint：用 KeepAlive 钉在最后一次使用之后。
	runtime.KeepAlive(&ps)
}

// onKeyDown 处理 WM_KEYDOWN：这些键全部由我们自己消费，不依赖控件子类化。
func (w *winWindow) onKeyDown(vk uint32) {
	switch vk {
	case vkUp:
		w.apply(uiKeyUp, 0)
	case vkDown:
		w.apply(uiKeyDown, 0)
	case vkPrior:
		w.apply(uiKeyPageUp, 0)
	case vkNext:
		w.apply(uiKeyPageDown, 0)
	case vkHome:
		w.apply(uiKeyHome, 0)
	case vkEnd:
		w.apply(uiKeyEnd, 0)
	case vkReturn:
		w.apply(uiKeyEnter, 0)
	case vkEscape:
		w.apply(uiKeyEscape, 0)
	case vkBack:
		w.apply(uiKeyBackspace, 0)
	}
}

// onChar 处理 WM_CHAR：可打印字符进入搜索词。
//
// 这里必须自己过滤控制字符：Esc/Enter/Backspace 也会以 WM_CHAR 到达
// （TranslateMessage 对所有字符键都生成 WM_CHAR），不挡掉就会变成搜索词里的
// 隐形字符，导致「明明有这条却搜不到」。
func (w *winWindow) onChar(unit uint16) {
	r, complete := w.surrogate.feed(unit)
	if !complete || !uiPrintable(r) {
		return
	}
	w.apply(uiKeyChar, r)
}

// onClick 处理鼠标左键：命中的行直接确认。
func (w *winWindow) onClick(lparam uintptr) {
	// GET_Y_LPARAM：lParam 的高 16 位是客户区 y（有符号 16 位）。
	y := int(int16((lparam >> 16) & 0xffff))
	if it, ok := w.ui.selectAt(y); ok {
		w.result = Result{ID: it.ID, OK: true}
		w.close()
		return
	}
	procInvalidateRect.Call(w.hwnd, 0, 0)
}

// onWheel 处理滚轮：高精度设备一次可能给不足 120，所以先累积再按整格滚动。
func (w *winWindow) onWheel(wparam uintptr) {
	delta := int(int16((wparam >> 16) & 0xffff))
	if delta == 0 {
		return
	}
	w.wheelAcc += delta
	rows := w.wheelAcc / wheelDelta
	if rows == 0 {
		return
	}
	w.wheelAcc -= rows * wheelDelta
	// delta 为正表示往远离用户的方向滚（内容上移），列表相应往上翻。
	w.ui.scrollBy(-rows)
	procInvalidateRect.Call(w.hwnd, 0, 0)
}

// apply 把一次按键交给共享状态机，并按动作收尾。
func (w *winWindow) apply(k uiKey, r rune) {
	res, act := w.ui.handleKey(k, r)
	switch act {
	case uiActionConfirm:
		w.result = res
		w.close()
		return
	case uiActionCancel:
		w.result = Result{}
		w.close()
		return
	}
	if k == uiKeyEnter && w.ui.count() == 0 {
		// 没有可确认的条目：响一声，免得用户以为按键没生效。
		procMessageBeep.Call(0)
	}
	procInvalidateRect.Call(w.hwnd, 0, 0)
}

// close 结束窗口：DestroyWindow → WM_DESTROY → PostQuitMessage → 消息循环退出。
func (w *winWindow) close() {
	if w.closed {
		return
	}
	w.closed = true
	procDestroyWindow.Call(w.hwnd)
}

// paint 画整个窗口：标题、搜索行、列表（含高亮）、底部统计、边框。
func (w *winWindow) paint(hwnd uintptr) {
	var ps winPaintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	runtime.KeepAlive(&ps)
	if hdc == 0 {
		return
	}
	// 用闭包而不是 defer EndPaint(... &ps)：闭包捕获的是真实指针，
	// GC 能看见它，ps 会被保证活到函数结束；直接把 &ps 转成 uintptr 交给
	// defer 则丢掉了这层信息。
	defer func() {
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	}()

	var client winRect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client)))
	runtime.KeepAlive(&client)

	p := winPainter{hdc: hdc}
	p.fill(client, w.brBg)
	if w.font != 0 {
		procSelectObject.Call(hdc, w.font)
	}
	// 文字都「画在背景上」而不是「画在文字底块上」，否则 CJK 会带小方块底色。
	procSetBkMode.Call(hdc, winTransparent)

	// 标题行：单个短文本，用 TextOutW 精确落点。
	p.textOut(int32(uiPad), int32(uiPad+4), w.title, colText)
	// 搜索行
	p.text(w.band(uiListTop-uiQueryH, uiListTop-8), w.ui.queryLine(), colDim)
	// 标题与列表之间的分隔线
	line := winRect{int32(uiPad), int32(uiListTop - 8), int32(uiWidth - uiPad), int32(uiListTop - 7)}
	p.fill(line, w.brLine)

	w.paintRows(p)

	// 底部统计
	p.text(w.band(uiListBottom, uiListBottom+uiFooterH), w.ui.summary(), colDim)

	// 窗口外框：无边框窗口没有系统边框，自己画一圈才不至于和背景糊在一起。
	procFrameRect.Call(hdc, uintptr(unsafe.Pointer(&client)), w.brLine)
}

// band 返回横向铺满、纵向 [top, bottom) 的矩形。
func (w *winWindow) band(top, bottom int) winRect {
	return winRect{int32(uiPad), int32(top), int32(uiWidth - uiPad), int32(bottom)}
}

// paintRows 画列表区：高亮当前行 + 每行「内容预览 + 右对齐的来源/时间」。
func (w *winWindow) paintRows(p winPainter) {
	if w.ui.count() == 0 {
		// 有历史但搜索词没命中（或历史为空）：在列表区中央给一句话。
		p.textIn(winRect{int32(uiPad), int32(uiListTop), int32(uiWidth - uiPad), int32(uiListBottom)},
			uiEmptyHint, colDim, dtCenter)
		return
	}
	for slot := 0; slot < uiVisibleRows; slot++ {
		idx := w.ui.scroll + slot
		if idx >= w.ui.count() {
			break
		}
		top := int32(uiRowTop(slot))
		// 高亮铺满整个宽度（含两侧留白），视觉上是一条完整的选中条。
		if idx == w.ui.sel {
			p.fill(winRect{0, top, int32(uiWidth), top + int32(uiRowH)}, w.brSel)
		}
		textColor, metaColor := colText, colDim
		if idx == w.ui.sel {
			textColor, metaColor = colSelText, colSelText
		}
		body, meta := uiRowText(w.ui.shown[idx])
		row := winRect{int32(uiPad), top, int32(uiWidth - uiPad), top + int32(uiRowH)}
		metaWidth := p.measure(meta)
		if metaWidth > winMaxChromeWidth {
			metaWidth = winMaxChromeWidth
		}
		metaRect := winRect{row.right - metaWidth, row.top, row.right, row.bottom}
		bodyRect := winRect{row.left, row.top, metaRect.left - 8, row.bottom}
		if metaWidth > 0 && bodyRect.right > bodyRect.left {
			p.text(bodyRect, body, textColor)
			p.textRight(metaRect, meta, metaColor)
			continue
		}
		// 窗口太窄或次要信息太长时，宁可只显示内容，也不要把内容截没。
		p.text(row, body, textColor)
	}
}

// winPainter 是 GDI 绘制的薄封装：所有绘制都经过这里，方便统一设置颜色。
type winPainter struct{ hdc uintptr }

// fill 用画刷填充矩形。
func (p winPainter) fill(r winRect, brush uintptr) {
	procFillRect.Call(p.hdc, uintptr(unsafe.Pointer(&r)), brush)
}

// textOut 在 (x, y) 处画一段文本（y 是字符单元格顶部，TextOutW 的语义）。
func (p winPainter) textOut(x, y int32, s string, color uintptr) {
	u := syscall.StringToUTF16(s)
	procSetTextColor.Call(p.hdc, color)
	procTextOutW.Call(p.hdc,
		uintptr(x), uintptr(y),
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1))
	runtime.KeepAlive(u)
}

// text 在矩形内左对齐画一行文本（超宽自动省略号）。
func (p winPainter) text(r winRect, s string, color uintptr) {
	p.textIn(r, s, color, dtLeft)
}

// textRight 在矩形内右对齐画一行文本。
func (p winPainter) textRight(r winRect, s string, color uintptr) {
	p.textIn(r, s, color, dtRight)
}

// textIn 在矩形内画一行文本，垂直居中，过长用省略号收尾。
//
// 用 DrawTextW 而不是 TextOutW 是因为这里需要「限定宽度的裁剪 + 省略号」：
// 剪贴板内容可能是一整段代码，自己算像素宽度不如交给 GDI。
// DT_NOPREFIX 必须带：否则内容里的 '&' 会被当成助记符吃掉。
func (p winPainter) textIn(r winRect, s string, color uintptr, align uintptr) {
	u := syscall.StringToUTF16(s)
	procSetTextColor.Call(p.hdc, color)
	procDrawTextW.Call(p.hdc,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1),
		uintptr(unsafe.Pointer(&r)),
		align|dtVCenter|dtSingleLine|dtNoPrefix|dtEndEllipsis)
	runtime.KeepAlive(u)
	runtime.KeepAlive(&r)
}

// measure 返回一段文本在当前字体下的像素宽度。
func (p winPainter) measure(s string) int32 {
	if s == "" {
		return 0
	}
	u := syscall.StringToUTF16(s)
	var size winSize
	procGetTextExtentPoint32.Call(p.hdc,
		uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&size)))
	runtime.KeepAlive(u)
	runtime.KeepAlive(&size)
	return size.cx
}

// createSolidBrush 建一把实心画刷；0 表示失败。
func createSolidBrush(color uintptr) uintptr {
	h, _, _ := procCreateSolidBrush.Call(color)
	return h
}

// createUIFont 按「Microsoft YaHei UI → Segoe UI → 系统默认 GUI 字体」的顺序
// 挑一个能用的字体，返回句柄与「是否由我们创建」（决定能不能 DeleteObject）。
//
// 为什么不能只看 CreateFontW 的返回值：它对不存在的字体名**不会失败**，
// 而是悄悄替换成最接近的字体（所以「优先 YaHei UI」光看返回值是自我安慰）。
// 必须用 GetTextFaceW 问一句「你实际给我的字体叫什么」，名字对不上才换下一个。
//
// 名字对不上也不能直接跳到最后：中文系统上字体会回报本地化名（例如
// 「微软雅黑」），严格比较会让每次弹窗都掉到 DEFAULT_GUI_FONT，观感反而更差。
// 所以「名字对不上但能创建」的第一个候选会被保留下来使用。
func createUIFont(height int32) (hfont uintptr, owned bool) {
	var fallback uintptr
	for _, face := range []string{"Microsoft YaHei UI", "Segoe UI"} {
		h := createFont(face, height)
		if h == 0 {
			continue
		}
		if uiFaceMatches(face, winRealizedFace(h)) {
			if fallback != 0 {
				procDeleteObject.Call(fallback)
			}
			return h, true
		}
		if fallback == 0 {
			fallback = h
		} else {
			procDeleteObject.Call(h)
		}
	}
	if fallback != 0 {
		return fallback, true
	}
	// 最后兜底：GDI 的默认 GUI 字体。它是 stock object，不能 DeleteObject。
	if stock, _, _ := procGetStockObject.Call(stockDefaultGUIFont); stock != 0 {
		return stock, false
	}
	// 连 stock object 都拿不到：返回 0，SelectObject 传 0 不会改动 DC 的当前字体，
	// 文字还能用窗口的默认字体画出来（不完美但不会空白）。
	return 0, false
}

// createFont 创建一个逻辑字体；0 表示失败。
func createFont(face string, height int32) uintptr {
	facePtr, err := syscall.UTF16PtrFromString(face)
	if err != nil {
		return 0
	}
	h, _, _ := procCreateFontW.Call(
		uintptr(height),      // cHeight：负值 = 字符高度
		0,                    // cWidth：0 = 按字体默认比例
		0,                    // cEscapement
		0,                    // cOrientation
		fwNormal,             // cWeight
		0,                    // bItalic
		0,                    // bUnderline
		0,                    // bStrikeOut
		defaultCharset,       // iCharSet
		outDefaultPrecision,  // iOutPrecision
		clipDefaultPrecision, // iClipPrecision
		cleartypeQuality,     // iQuality
		pitchAndFamily,       // iPitchAndFamily
		uintptr(unsafe.Pointer(facePtr)),
	)
	runtime.KeepAlive(facePtr)
	return h
}

// winRealizedFace 返回字体句柄真正生效的字体族名（空字符串表示问不出来）。
func winRealizedFace(hfont uintptr) string {
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return ""
	}
	old, _, _ := procSelectObject.Call(hdc, hfont)
	buf := make([]uint16, 64)
	n, _, _ := procGetTextFaceW.Call(hdc, uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])))
	runtime.KeepAlive(buf)
	if old != 0 {
		procSelectObject.Call(hdc, old)
	}
	procReleaseDC.Call(0, hdc)
	if int32(n) <= 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
