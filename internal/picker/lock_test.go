package picker

import (
	"path/filepath"
	"testing"
)

func TestSingleInstanceExcludesSecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "picker.lock")

	release, ok := SingleInstance(path)
	if !ok {
		t.Fatal("首次获取锁应当成功")
	}

	// 第二次必须拿不到（同进程内 flock 由 fd 区分，这里模拟另一个进程的语义）
	if _, ok2 := SingleInstance(path); ok2 {
		t.Fatal("已有实例在跑时，第二次获取应失败")
	}

	release()

	release3, ok3 := SingleInstance(path)
	if !ok3 {
		t.Fatal("释放后应能再次获取")
	}
	release3()
}

func TestSingleInstanceEmptyPath(t *testing.T) {
	release, ok := SingleInstance("")
	if !ok {
		t.Fatal("空路径不应阻止运行")
	}
	release()
}
