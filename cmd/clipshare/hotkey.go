package main

import (
	"errors"
	"log"

	"clipshare/internal/config"
	"clipshare/internal/engine"
	"clipshare/internal/hotkey"
	"clipshare/internal/picker"
)

// startHotkey 让常驻进程接管全局热键（Ctrl+Alt+V）并返回停止函数。
//
// 平台差异：
//   - Windows：进程内 RegisterHotKey，按下时直接调 picker.Run（同一线程建窗口）；
//   - 其它平台：hotkey.Start 立刻返回 ErrUnsupported，快捷键由桌面环境绑定到
//     `clipshare pick`（见 README），本函数静默返回空停止函数。
//
// 只在常驻（托盘）模式下调用：`--no-tray` 通常用于服务器/无桌面环境，
// 那里注册热键只会产生噪音日志。
func startHotkey(eng *engine.Engine, cfg *config.Config, cfgDir string) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := hotkey.Start(func() { runPickInProcess(eng, cfg, cfgDir) })
		if err != nil && !errors.Is(err, hotkey.ErrUnsupported) {
			log.Printf("警告: 全局热键注册失败（不影响同步）: %v", err)
		}
	}()
	return func() {
		hotkey.Stop()
		<-done
	}
}

// runPickInProcess 是热键回调：在进程内弹出选择窗口并取回选中项。
//
// 与 `clipshare pick` 的区别只是「拿数据的通道」：这里直接读 engine，
// 子命令走 loopback HTTP；取回语义完全相同（S3：写剪贴板但不广播）。
func runPickInProcess(eng *engine.Engine, cfg *config.Config, cfgDir string) {
	entries := eng.ListEntries()
	if len(entries) == 0 {
		return
	}
	if err := picker.Run(entries, eng.PullEntry, cfg.DeviceID, picker.Options{
		PosFile: pickerPosFile(cfgDir),
	}); err != nil {
		log.Printf("选择窗口失败: %v", err)
	}
}
