// Package model 定义剪贴板条目与内容归一化规则。
//
// 本包不含任何 I/O，是 history / sync / engine 共享的纯数据类型。
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

// Entry 是一条剪贴板历史记录。
//
// 排序与展示一律使用 RecvAt（本地接收/产生时间），不使用发送方时钟，
// 以避免跨机时钟漂移导致的乱序。
type Entry struct {
	ID         string    `json:"id"`
	Origin     string    `json:"origin"`     // 产生该条目的节点 ID；空 = 本机
	OriginName string    `json:"originName"` // 展示用节点名
	Content    string    `json:"content"`
	RecvAt     time.Time `json:"recvAt"`
}

// Normalize 归一化剪贴板文本。
//
// 只做不影响语义的改动：统一换行、去掉尾部的 NUL（X11 有些程序会带）。
// 不做 TrimSpace——用户可能复制带缩进的代码。
func Normalize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, "\x00")
}

// ContentHash 返回内容指纹，用于跨机去重。
func ContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// RoomHash 返回房间标识，用于 mDNS TXT 与请求头中的房间校验。
// 密钥本身永不出网。
func RoomHash(roomKey string) string {
	sum := sha256.Sum256([]byte(roomKey))
	return hex.EncodeToString(sum[:])[:12]
}

// NewEntry 构造一条本机产生的条目。
func NewEntry(origin, originName, content string, at time.Time) Entry {
	sum := sha256.Sum256([]byte(origin + "\x00" + content))
	return Entry{
		ID:         hex.EncodeToString(sum[:])[:16],
		Origin:     origin,
		OriginName: originName,
		Content:    content,
		RecvAt:     at,
	}
}

// Truncate 把内容截成单行预览，供托盘菜单等宽度受限的位置使用。
func Truncate(content string, limit int) string {
	line := content
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i] + " ⏎"
	}
	runes := []rune(line)
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit]) + "…"
}
