// Package history 维护本机剪贴板历史：环形上限、内容去重、原子落盘。
//
// 并发安全。同一内容再次出现时不新增条目，而是把它置顶并刷新 RecvAt
// （与主流剪贴板管理器一致，同时天然充当跨机去重）。
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"clipshare/internal/model"
)

// persistLogInterval 是落盘失败日志的节流间隔，避免磁盘满时刷屏。
const persistLogInterval = time.Minute

// Store 是剪贴板历史。零值不可用，请用 New。
type Store struct {
	mu      sync.Mutex
	entries []model.Entry // 最新在前
	limit   int
	path    string // "" 表示纯内存
	persist bool

	lastPersistErr error
	lastPersistLog time.Time
}

// New 创建历史仓库。path 为空表示不落盘；persist 为 false 时也不会写文件。
func New(limit int, path string, persist bool) *Store {
	if limit < 1 {
		limit = 1
	}
	return &Store{limit: limit, path: path, persist: persist}
}

// SetLimit 调整历史条数上限，超出部分立即裁掉。
func (s *Store) SetLimit(limit int) {
	if limit < 1 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.limit = limit
	if len(s.entries) > limit {
		s.entries = s.entries[:limit]
	}
}

// SetPersist 开关落盘。开启时立即写一次。
func (s *Store) SetPersist(persist bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persist = persist
	if persist {
		s.flushLocked()
	}
}

// Load 从磁盘载入历史。文件不存在视为空历史；
// 文件损坏时从空历史起步并返回错误（调用方只应告警，不应阻断启动）。
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取历史失败: %w", err)
	}
	var loaded []model.Entry
	if err := json.Unmarshal(raw, &loaded); err != nil {
		s.entries = nil
		return fmt.Errorf("历史文件损坏，已从空历史起步: %w", err)
	}
	seen := make(map[string]struct{}, len(loaded))
	s.entries = s.entries[:0]
	for _, e := range loaded {
		if e.Content == "" {
			continue
		}
		e.Content = model.Normalize(e.Content)
		h := model.ContentHash(e.Content)
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		s.entries = append(s.entries, e)
		if len(s.entries) >= s.limit {
			break
		}
	}
	return nil
}

// Add 加入一条记录。
//
// 返回 added：内容是否首次出现。已存在时只把旧条目置顶并刷新 RecvAt，
// 保留原始来源信息，返回 false。调用方据此决定是否广播。
func (s *Store) Add(e model.Entry) bool {
	e.Content = model.Normalize(e.Content)
	if e.Content == "" {
		return false
	}
	if e.RecvAt.IsZero() {
		e.RecvAt = time.Now()
	}
	if e.ID == "" {
		e.ID = model.NewEntry(e.Origin, e.OriginName, e.Content, e.RecvAt).ID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if idx := s.indexOfLocked(e.Content); idx >= 0 {
		s.entries[idx].RecvAt = e.RecvAt
		bumped := s.entries[idx]
		copy(s.entries[1:idx+1], s.entries[0:idx])
		s.entries[0] = bumped
		s.flushLocked()
		return false
	}

	s.entries = append([]model.Entry{e}, s.entries...)
	if len(s.entries) > s.limit {
		s.entries = s.entries[:s.limit]
	}
	s.flushLocked()
	return true
}

// List 返回最新在前的最多 limit 条记录（limit<=0 表示全部）。
func (s *Store) List(limit int) []model.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.entries)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]model.Entry, n)
	copy(out, s.entries[:n])
	return out
}

// Get 按 ID 取一条记录。
func (s *Store) Get(id string) (model.Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return model.Entry{}, false
}

// Count 返回当前条数。
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Clear 清空历史并落盘。
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	if !s.persist || s.path == "" {
		return nil
	}
	return s.writeLocked()
}

// LastError 返回最近一次落盘错误（落盘失败不影响内存历史继续可用）。
func (s *Store) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPersistErr
}

func (s *Store) indexOfLocked(content string) int {
	for i, e := range s.entries {
		if e.Content == content {
			return i
		}
	}
	return -1
}

// flushLocked 尽量落盘；失败只记日志（节流），内存历史保持可用。
func (s *Store) flushLocked() {
	if !s.persist || s.path == "" {
		return
	}
	if err := s.writeLocked(); err != nil {
		s.lastPersistErr = err
		if time.Since(s.lastPersistLog) >= persistLogInterval {
			s.lastPersistLog = time.Now()
			log.Printf("警告: 历史落盘失败（内存历史继续可用）: %v", err)
		}
	} else {
		s.lastPersistErr = nil
	}
}

func (s *Store) writeLocked() error {
	raw, err := json.Marshal(s.entries)
	if err != nil {
		return fmt.Errorf("序列化历史失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("创建历史目录失败: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入历史失败: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("替换历史文件失败: %w", err)
	}
	return nil
}
