package history

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"clipshare/internal/model"
)

func entry(content, origin string, at time.Time) model.Entry {
	return model.NewEntry(origin, "PC-"+origin, content, at)
}

func TestAddEnforcesLimit(t *testing.T) {
	s := New(3, "", false)
	base := time.Unix(1700000000, 0)
	for i, c := range []string{"one", "two", "three", "four", "five"} {
		s.Add(entry(c, "local", base.Add(time.Duration(i)*time.Second)))
	}
	got := s.List(0)
	if len(got) != 3 {
		t.Fatalf("条数 = %d, want 3", len(got))
	}
	want := []string{"five", "four", "three"}
	for i := range want {
		if got[i].Content != want[i] {
			t.Errorf("第 %d 条 = %q, want %q", i, got[i].Content, want[i])
		}
	}
}

func TestDuplicateBumpsAndKeepsOrigin(t *testing.T) {
	s := New(10, "", false)
	t0 := time.Unix(1700000000, 0)
	s.Add(entry("dup", "remote", t0))
	s.Add(entry("other", "local", t0.Add(time.Second)))

	added := s.Add(entry("dup", "local", t0.Add(2*time.Second)))
	if added {
		t.Fatal("重复内容应返回 added=false（调用方据此不广播）")
	}
	got := s.List(0)
	if len(got) != 2 {
		t.Fatalf("条数 = %d, want 2（不应新增条目）", len(got))
	}
	if got[0].Content != "dup" {
		t.Fatalf("重复内容应置顶，实际首位 = %q", got[0].Content)
	}
	if got[0].Origin != "remote" {
		t.Errorf("置顶后应保留原始来源，实际 = %q", got[0].Origin)
	}
	if !got[0].RecvAt.Equal(t0.Add(2 * time.Second)) {
		t.Errorf("RecvAt 应刷新为最新一次，实际 = %v", got[0].RecvAt)
	}
}

func TestEmptyContentIgnored(t *testing.T) {
	s := New(10, "", false)
	if added := s.Add(model.Entry{Content: ""}); added {
		t.Fatal("空内容不应入历史")
	}
	if s.Count() != 0 {
		t.Fatalf("条数 = %d, want 0", s.Count())
	}
}

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	s := New(10, path, true)
	now := time.Now().UTC().Truncate(time.Second)
	s.Add(entry("persisted", "local", now))

	reloaded := New(10, path, true)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := reloaded.List(0)
	if len(got) != 1 || got[0].Content != "persisted" {
		t.Fatalf("重载结果 = %+v", got)
	}
	if !got[0].RecvAt.Equal(now) {
		t.Errorf("RecvAt = %v, want %v", got[0].RecvAt, now)
	}
	if len(got[0].ID) != 16 {
		t.Errorf("重载后 ID 丢失: %q", got[0].ID)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	s := New(10, filepath.Join(t.TempDir(), "nope.json"), true)
	if err := s.Load(); err != nil {
		t.Fatalf("文件不存在不应报错: %v", err)
	}
	if s.Count() != 0 {
		t.Fatal("应为空历史")
	}
}

func TestLoadCorruptStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(10, path, true)
	if err := s.Load(); err == nil {
		t.Fatal("损坏文件应返回错误供调用方告警")
	}
	if s.Count() != 0 {
		t.Fatal("损坏文件应从空历史起步")
	}
}

func TestPersistDisabledWritesNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	s := New(10, path, false)
	s.Add(entry("x", "local", time.Now()))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("persist=false 时不应写文件, stat err = %v", err)
	}
	// 但内存中仍应有记录
	if s.Count() != 1 {
		t.Fatal("内存历史应照常工作")
	}
}

func TestClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history.json")
	s := New(10, path, true)
	s.Add(entry("a", "local", time.Now()))
	if err := s.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if s.Count() != 0 {
		t.Fatal("清空后应为 0")
	}
	reloaded := New(10, path, true)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	if reloaded.Count() != 0 {
		t.Fatal("清空后重载应为空")
	}
}

func TestGet(t *testing.T) {
	s := New(10, "", false)
	e := entry("findme", "local", time.Now())
	s.Add(e)
	got, ok := s.Get(e.ID)
	if !ok || got.Content != "findme" {
		t.Fatalf("Get 失败: %+v ok=%v", got, ok)
	}
	if _, ok := s.Get("missing"); ok {
		t.Fatal("不存在的 ID 应返回 false")
	}
}

func TestConcurrentAdd(t *testing.T) {
	s := New(100, filepath.Join(t.TempDir(), "h.json"), true)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				s.Add(entry(string(rune('a'+i))+string(rune('0'+j)), "local", time.Now()))
			}
		}(i)
	}
	wg.Wait()
	if got := s.Count(); got != 100 {
		t.Fatalf("并发写入后条数 = %d, want 100（受 limit 限制）", got)
	}
	if s.LastError() != nil {
		t.Fatalf("并发落盘不应出错: %v", s.LastError())
	}
}

func TestListLimitAndCopy(t *testing.T) {
	s := New(10, "", false)
	s.Add(entry("a", "local", time.Now()))
	s.Add(entry("b", "local", time.Now().Add(time.Second)))
	got := s.List(1)
	if len(got) != 1 || got[0].Content != "b" {
		t.Fatalf("List(1) = %+v", got)
	}
	got[0].Content = "mutated"
	if again := s.List(1); again[0].Content != "b" {
		t.Fatal("List 必须返回副本，不能暴露内部切片")
	}
}
