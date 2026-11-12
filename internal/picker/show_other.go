//go:build !linux && !windows

package picker

import "errors"

// show 在未支持的平台返回明确错误：本功能依赖平台原生窗口。
func show(items []Item, opts Options) (Result, error) {
	return Result{}, errors.New("当前平台不支持图形选择窗口")
}
