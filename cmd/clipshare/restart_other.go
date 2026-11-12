//go:build !linux && !windows

package main

import (
	"errors"
	"os"
)

func selfCommand() (string, []string, error) {
	exe, err := os.Executable()
	return exe, os.Args[1:], err
}

// spawnDetached 在未支持的平台上明确报错，而不是假装重启成功。
func spawnDetached(string, []string) error {
	return errors.New("当前平台不支持自动重启，请手动重启 clipshare")
}
