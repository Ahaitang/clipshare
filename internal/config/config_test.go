package config

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestLoadCreatesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := os.Stat(Path(dir)); err != nil {
		t.Fatalf("首次加载应生成配置文件: %v", err)
	}
	if cfg.Port != DefaultPort || cfg.HistoryLimit != DefaultHistoryLimit {
		t.Errorf("默认值不正确: %+v", cfg)
	}
	if !cfg.AutoApply || !cfg.MDNS || !cfg.Persist {
		t.Errorf("默认应为三个 true: %+v", cfg)
	}
	if len(cfg.DeviceID) != 32 {
		t.Errorf("DeviceID 应为 32 位 hex，实际 %q", cfg.DeviceID)
	}
	if len(cfg.RoomKey) != roomKeyLen {
		t.Errorf("RoomKey 应为 %d 位，实际 %q", roomKeyLen, cfg.RoomKey)
	}
	if strings.TrimSpace(cfg.NodeName) == "" {
		t.Error("NodeName 不应为空")
	}
	// 文件权限必须是 0600（里面存了房间密钥）
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("配置权限 = %o, want 600", mode)
	}
}

func TestLoadIsStableAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	first, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.DeviceID != second.DeviceID || first.RoomKey != second.RoomKey {
		t.Fatalf("二次加载应复用身份: %q/%q vs %q/%q",
			first.DeviceID, first.RoomKey, second.DeviceID, second.RoomKey)
	}
}

func TestPartialConfigFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	partial := []byte(`{"port": 48100, "nodeName": "MYPC"}`)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), partial, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 48100 || cfg.NodeName != "MYPC" {
		t.Errorf("显式字段未生效: %+v", cfg)
	}
	// 关键：缺省且默认 true 的布尔项必须仍是 true
	if !cfg.AutoApply || !cfg.MDNS || !cfg.Persist {
		t.Errorf("缺省布尔项应回落到 true，实际 %+v", cfg)
	}
	if cfg.HistoryLimit != DefaultHistoryLimit || cfg.PollIntervalMs != DefaultPollIntervalMs {
		t.Errorf("缺省数值项应回落默认值: %+v", cfg)
	}
	if cfg.DeviceID == "" {
		t.Error("缺 DeviceID 时应补生成")
	}
}

func TestExplicitFalseIsRespected(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"autoApply": false, "mdns": false, "persist": false}`)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), body, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutoApply || cfg.MDNS || cfg.Persist {
		t.Fatalf("显式 false 被覆盖为 true: %+v", cfg)
	}
}

func TestCorruptConfigBackedUpAndRebuilt(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(dir), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err == nil {
		t.Fatal("损坏配置应返回错误供调用方告警")
	}
	if cfg == nil || cfg.Port != DefaultPort {
		t.Fatalf("损坏后应返回默认配置: %+v", cfg)
	}
	if _, statErr := os.Stat(Path(dir) + ".bak"); statErr != nil {
		t.Fatalf("应留下 .bak 备份: %v", statErr)
	}
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("重建后的配置应可正常加载: %v", err)
	}
	if reloaded.Port != DefaultPort {
		t.Fatalf("重建配置内容异常: %+v", reloaded)
	}
}

func TestValidate(t *testing.T) {
	base := Default()
	cases := []struct {
		name   string
		mutate func(*Config)
		wantOK bool
	}{
		{"ok", func(c *Config) {}, true},
		{"port too low", func(c *Config) { c.Port = 0 }, false},
		{"port too high", func(c *Config) { c.Port = 70000 }, false},
		{"empty room", func(c *Config) { c.RoomKey = "  " }, false},
		{"historyLimit zero", func(c *Config) { c.HistoryLimit = 0 }, false},
		{"itemMaxBytes tiny", func(c *Config) { c.ItemMaxBytes = 10 }, false},
		{"poll too fast", func(c *Config) { c.PollIntervalMs = 10 }, false},
		{"seed ok", func(c *Config) { c.Seeds = []string{"192.168.1.7:47100"} }, true},
		{"seed no port", func(c *Config) { c.Seeds = []string{"192.168.1.7"} }, false},
		{"seed bad port", func(c *Config) { c.Seeds = []string{"192.168.1.7:abc"} }, false},
	}
	for _, c := range cases {
		cfg := *base
		c.mutate(&cfg)
		err := cfg.Validate()
		if (err == nil) != c.wantOK {
			t.Errorf("%s: Validate() err = %v, wantOK = %v", c.name, err, c.wantOK)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.Port = 49000
	cfg.Seeds = []string{"10.0.0.5:49000"}
	cfg.AutoApply = false
	cfg.NoTray = true
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if back.Port != 49000 || len(back.Seeds) != 1 || back.AutoApply || !back.NoTray {
		t.Fatalf("往返后不一致: %+v", back)
	}
	if back.DeviceID != cfg.DeviceID || back.RoomKey != cfg.RoomKey {
		t.Fatal("身份信息应原样往返")
	}
	raw, _ := os.ReadFile(Path(dir))
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("落盘内容必须是合法 JSON: %v", err)
	}
	for _, key := range []string{"nodeName", "deviceId", "roomKey", "port", "autoApply", "pollIntervalMs"} {
		if _, ok := probe[key]; !ok {
			t.Errorf("落盘配置缺少字段 %q（应完整写出而非省略）", key)
		}
	}
}

func TestBaseURL(t *testing.T) {
	if got := BaseURL("192.168.1.7:47100"); got != "http://192.168.1.7:47100" {
		t.Errorf("BaseURL = %q", got)
	}
	if got := BaseURL("http://10.0.0.1:47100/"); got != "http://10.0.0.1:47100" {
		t.Errorf("BaseURL 应去掉尾部斜杠, 实际 %q", got)
	}
}

func TestRoomHashDoesNotLeakKey(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := cfg.RoomHashValue()
	if len(h) != 12 {
		t.Fatalf("roomHash 长度 = %d", len(h))
	}
	if strings.Contains(h, cfg.RoomKey) {
		t.Fatal("roomHash 不得包含密钥原文")
	}
	if h == cfg.RoomKey {
		t.Fatal("roomHash 不应等于密钥")
	}
}

// Load 必须在所有成功返回路径上填好 Dir（运行期字段），engine 保存开关时要复用它。
func TestLoadFillsRuntimeDir(t *testing.T) {
	t.Run("文件缺失、新建默认配置", func(t *testing.T) {
		dir := t.TempDir()
		cfg, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if cfg.Dir != dir {
			t.Fatalf("cfg.Dir = %q，期望 %q", cfg.Dir, dir)
		}
	})

	t.Run("正常加载已有文件", func(t *testing.T) {
		dir := t.TempDir()
		first, err := Load(dir)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if err := first.Save(dir); err != nil {
			t.Fatalf("Save: %v", err)
		}
		again, err := Load(dir)
		if err != nil {
			t.Fatalf("二次 Load: %v", err)
		}
		if again.Dir != dir {
			t.Fatalf("二次加载 cfg.Dir = %q，期望 %q", again.Dir, dir)
		}
	})

	t.Run("损坏后重建", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(Path(dir), []byte("{ 这不是 JSON"), 0o600); err != nil {
			t.Fatalf("写损坏配置: %v", err)
		}
		cfg, err := Load(dir)
		if err == nil {
			t.Fatal("损坏配置应返回告警错误")
		}
		if cfg == nil {
			t.Fatal("损坏重建路径仍应返回可用的默认配置")
		}
		if cfg.Dir != dir {
			t.Fatalf("重建后的 cfg.Dir = %q，期望 %q", cfg.Dir, dir)
		}
	})
}

// Dir 是运行期字段，绝不能落盘（否则两个实例会互相看到对方的历史目录）。
func TestDirNotPersisted(t *testing.T) {
	dir := t.TempDir()
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Dir = "/some/other/place"
	if err := cfg.Save(dir); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatalf("读配置: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析配置 JSON: %v", err)
	}
	if _, ok := m["dir"]; ok {
		t.Fatalf("配置 JSON 不应包含 dir 字段，实际内容:\n%s", raw)
	}
	if _, ok := m["Dir"]; ok {
		t.Fatalf("配置 JSON 不应包含 Dir 字段，实际内容:\n%s", raw)
	}
}

// TestNewRoomKeyLength 钉住房间码长度 = 12。
//
// 算术：房间码字母表 31 个字符。
//   - 6 位 → 31^6 ≈ 8.9×10^8 ≈ 29.7 bit
//   - 12 位 → 31^12 ≈ 7.9×10^17 ≈ 59.5 bit
//
// 而 roomHash = sha256(roomKey) 前 12 位 hex（48 bit）是公开的（/v1/health 免鉴权、
// mDNS TXT 也带），所以攻击者可以离线枚举候选 key 并用 hash 前缀验证：
// 6 位时枚举空间远小于 2^48，几分钟就能唯一命中；12 位才明显大于验证空间。
func TestNewRoomKeyLength(t *testing.T) {
	if roomKeyLen != 12 {
		t.Fatalf("roomKeyLen = %d，期望 12（31^12 ≈ 59.5 bit；6 位只有 ≈29.7 bit，hash 公开时可被离线爆破）", roomKeyLen)
	}
	for i := 0; i < 100; i++ {
		key := NewRoomKey()
		if len(key) != roomKeyLen {
			t.Fatalf("NewRoomKey() = %q，长度 %d，期望 %d", key, len(key), roomKeyLen)
		}
	}
}

// TestNewRoomKeyAlphabet：房间码只由去混淆字母表组成（方便手抄，不含 0/O/1/l/i）。
func TestNewRoomKeyAlphabet(t *testing.T) {
	for i := 0; i < 200; i++ {
		key := NewRoomKey()
		for _, r := range key {
			if !strings.ContainsRune(roomKeyAlphabet, r) {
				t.Fatalf("房间码 %q 含字母表外字符 %q（字母表 %q）", key, r, roomKeyAlphabet)
			}
		}
	}
}

// TestNewRoomKeyUnique：批量生成不应出现重复（12 位空间下 2000 个样本重复概率可忽略，
// 这条主要防「随机源用错/只填了部分字节」这类实现退化）。
func TestNewRoomKeyUnique(t *testing.T) {
	const n = 2000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		key := NewRoomKey()
		if _, dup := seen[key]; dup {
			t.Fatalf("第 %d 次生成出现重复房间码 %q", i, key)
		}
		seen[key] = struct{}{}
	}
	if len(seen) != n {
		t.Fatalf("去重后只有 %d 个，期望 %d", len(seen), n)
	}
}

// TestValidateAcceptsShortLegacyRoomKey：向后兼容——Validate 不设长度下限，
// 已部署的 6 位房间码必须继续能用（只在文档里建议换长）。
func TestValidateAcceptsShortLegacyRoomKey(t *testing.T) {
	for _, key := range []string{"abc123", "abcdefghjkmn", NewRoomKey()} {
		cfg := *Default()
		cfg.RoomKey = key
		if err := cfg.Validate(); err != nil {
			t.Fatalf("roomKey=%q 应通过校验（不设长度下限）: %v", key, err)
		}
	}
}
