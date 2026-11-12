package main

import (
	"log"
	"sync/atomic"
)

// restartCh 由「重启」按钮触发（托盘菜单 / 本地历史页）。
//
// 采用「先正常退出、再重新拉起自己」而不是原地 exec：正常退出会让原有的
// defer 依次跑完（engine.Stop 停轮询与 HTTP、history 落盘、UI listener 关闭），
// 避免重启过程中写坏 history.json 或端口没释放导致新进程起不来。
var (
	// restartCh 只当「叫醒信号」用：谁在阻塞等待就通知谁。
	restartCh = make(chan struct{}, 1)
	// restartFlag 记录「这次退出是不是为了重启」，退出后由 run() 判断要不要重新拉起。
	// 不能只看 channel：channel 会被等待方消费掉，信息就丢了。
	restartFlag atomic.Bool
)

// requestRestart 请求一次重启；重复请求会被合并。
func requestRestart() {
	restartFlag.Store(true)
	select {
	case restartCh <- struct{}{}:
	default: // 已经排了重启请求，不重复入队
	}
}

// restartRequested 报告本次退出是否由「重启」触发。
func restartRequested() bool { return restartFlag.Load() }

// relaunch 用同一套参数重新启动自己。平台相关实现见 restart_*.go。
//
// 返回 nil 表示「新的实例已经交出去」，调用方应当正常退出当前进程。
func relaunch() error {
	exe, args, err := selfCommand()
	if err != nil {
		return err
	}
	log.Printf("正在重启：%s %v", exe, args)
	return spawnDetached(exe, args)
}
