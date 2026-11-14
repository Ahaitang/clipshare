package engine

import (
	"testing"

	"clipshare/internal/config"
	"clipshare/internal/history"
)

// 中继状态必须能从快照里读出来：托盘的勾选项完全依赖它。
// 漏注入的后果是「勾选项永远未勾选，点了只会再启用一次，看起来毫无反应」（实测踩到）。
func TestRelayStatusAppearsInSnapshot(t *testing.T) {
	cfg := config.Default()
	store := history.New(10, "", false)
	e := New(cfg, store, &fakeClip{}, func(string, ...any) {})

	snap := e.Snapshot()
	if snap.RelayURL != "" || snap.RelayEnabled || snap.RelayConnected {
		t.Fatalf("未注入时中继状态应为空: %+v", snap)
	}

	e.SetRelayStatus("https://a.example.com:8443", true, func() bool { return true })
	snap = e.Snapshot()
	if snap.RelayURL != "https://a.example.com:8443" {
		t.Errorf("RelayURL = %q", snap.RelayURL)
	}
	if !snap.RelayEnabled {
		t.Error("RelayEnabled 应为 true")
	}
	if !snap.RelayConnected {
		t.Error("RelayConnected 应为 true")
	}

	// 停用但保留地址：勾选项应变未勾选，地址仍可见
	e.SetRelayStatus("https://a.example.com:8443", false, func() bool { return false })
	snap = e.Snapshot()
	if snap.RelayEnabled || snap.RelayConnected {
		t.Errorf("停用后应为 false: %+v", snap)
	}
	if snap.RelayURL == "" {
		t.Error("停用时地址仍应保留，便于再次启用")
	}
}
