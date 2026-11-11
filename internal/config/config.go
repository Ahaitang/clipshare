// Package config 负责配置文件的定位、加载、生成与校验。
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"clipshare/internal/model"
)

// 默认值。首次启动与配置缺字段时使用。
const (
	DefaultPort           = 47100
	DefaultHistoryLimit   = 500
	DefaultItemMaxBytes   = 262144
	DefaultPollIntervalMs = 400
	// roomKeyLen 是首启自动生成的房间码长度。
	roomKeyLen = 6
)

// roomKeyAlphabet 去掉了易混淆字符（0/O、1/l/i），方便口头或手抄。
const roomKeyAlphabet = "abcdefghjkmnpqrstuvwxyz23456789"

// Config 是运行期配置。所有字段都会被写回文件，不存在「缺省即省略」的情况。
type Config struct {
	NodeName       string   `json:"nodeName"`
	DeviceID       string   `json:"deviceId"`
	RoomKey        string   `json:"roomKey"`
	Port           int      `json:"port"`
	Seeds          []string `json:"seeds"`
	HistoryLimit   int      `json:"historyLimit"`
	ItemMaxBytes   int      `json:"itemMaxBytes"`
	AutoApply      bool     `json:"autoApply"`
	MDNS           bool     `json:"mdns"`
	Persist        bool     `json:"persist"`
	PollIntervalMs int      `json:"pollIntervalMs"`
	// NoTray 为 true 时不初始化托盘（无 DBus / 无 GNOME 扩展的环境）。
	NoTray bool `json:"noTray"`

	// Dir 是本次运行实际使用的配置目录（运行期字段，不写入 JSON）。
	// 由 Load 填入，供 engine 等处保存配置时复用，避免各处各自推算而分叉。
	Dir string `json:"-"`
}

// configJSON 用指针区分「字段缺失」与「显式零值」，
// 这样部分手写的配置也能正确回落到默认值（尤其是默认 true 的布尔项）。
type configJSON struct {
	NodeName       *string  `json:"nodeName"`
	DeviceID       *string  `json:"deviceId"`
	RoomKey        *string  `json:"roomKey"`
	Port           *int     `json:"port"`
	Seeds          []string `json:"seeds"`
	HistoryLimit   *int     `json:"historyLimit"`
	ItemMaxBytes   *int     `json:"itemMaxBytes"`
	AutoApply      *bool    `json:"autoApply"`
	MDNS           *bool    `json:"mdns"`
	Persist        *bool    `json:"persist"`
	PollIntervalMs *int     `json:"pollIntervalMs"`
	NoTray         *bool    `json:"noTray"`
}

// Dir 返回配置目录（不创建）。
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("定位用户配置目录失败: %w", err)
	}
	return filepath.Join(base, "clipshare"), nil
}

// Path 返回给定配置目录下的配置文件路径。
func Path(dir string) string { return filepath.Join(dir, "config.json") }

// HistoryPath 返回给定配置目录下的历史文件路径。
func HistoryPath(dir string) string { return filepath.Join(dir, "history.json") }

// Default 返回一份填好默认值的配置（含生成的 DeviceID 与 RoomKey）。
func Default() *Config {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "clipshare"
	}
	return &Config{
		NodeName:       host,
		DeviceID:       newDeviceID(),
		RoomKey:        NewRoomKey(),
		Port:           DefaultPort,
		Seeds:          []string{},
		HistoryLimit:   DefaultHistoryLimit,
		ItemMaxBytes:   DefaultItemMaxBytes,
		AutoApply:      true,
		MDNS:           true,
		Persist:        true,
		PollIntervalMs: DefaultPollIntervalMs,
	}
}

// Load 从 dir 读取配置。文件不存在则生成默认配置并落盘；
// 文件损坏则把原文件备份为 .bak 后用默认值重建。
//
// 无论走哪条返回路径，成功返回的 Config 都会带上 Dir = dir（运行期字段、不落盘），
// 调用方（如 engine 保存托盘开关）应复用它，而不是自己再去推算配置目录。
func Load(dir string) (*Config, error) {
	raw, err := os.ReadFile(Path(dir))
	if os.IsNotExist(err) {
		cfg := Default()
		cfg.Dir = dir
		if saveErr := cfg.Save(dir); saveErr != nil {
			return cfg, saveErr
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}

	var parsed configJSON
	if jsonErr := json.Unmarshal(raw, &parsed); jsonErr != nil {
		backup := Path(dir) + ".bak"
		_ = os.WriteFile(backup, raw, 0o600)
		cfg := Default()
		cfg.Dir = dir
		if saveErr := cfg.Save(dir); saveErr != nil {
			return cfg, fmt.Errorf("配置损坏（已备份到 %s）且重写失败: %w", backup, saveErr)
		}
		return cfg, fmt.Errorf("配置损坏，已备份到 %s 并重建默认配置", backup)
	}

	cfg := Default()
	cfg.Dir = dir
	cfg.DeviceID = "" // 缺失时下面补
	applyJSON(cfg, &parsed)
	if cfg.DeviceID == "" {
		cfg.DeviceID = newDeviceID()
	}
	if cfg.RoomKey == "" {
		cfg.RoomKey = NewRoomKey()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func applyJSON(cfg *Config, p *configJSON) {
	if p.NodeName != nil {
		cfg.NodeName = *p.NodeName
	}
	if p.DeviceID != nil {
		cfg.DeviceID = *p.DeviceID
	}
	if p.RoomKey != nil {
		cfg.RoomKey = *p.RoomKey
	}
	if p.Port != nil {
		cfg.Port = *p.Port
	}
	if p.Seeds != nil {
		cfg.Seeds = p.Seeds
	}
	if p.HistoryLimit != nil {
		cfg.HistoryLimit = *p.HistoryLimit
	}
	if p.ItemMaxBytes != nil {
		cfg.ItemMaxBytes = *p.ItemMaxBytes
	}
	if p.AutoApply != nil {
		cfg.AutoApply = *p.AutoApply
	}
	if p.MDNS != nil {
		cfg.MDNS = *p.MDNS
	}
	if p.Persist != nil {
		cfg.Persist = *p.Persist
	}
	if p.PollIntervalMs != nil {
		cfg.PollIntervalMs = *p.PollIntervalMs
	}
	if p.NoTray != nil {
		cfg.NoTray = *p.NoTray
	}
}

// Save 原子写入配置文件（权限 0600，含房间密钥）。
func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	raw = append(raw, '\n')
	tmp := Path(dir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := os.Rename(tmp, Path(dir)); err != nil {
		return fmt.Errorf("替换配置失败: %w", err)
	}
	return nil
}

// Validate 校验配置取值范围，给出可执行的错误信息。
func (c *Config) Validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port 必须在 1-65535 之间，当前 %d", c.Port)
	}
	if c.HistoryLimit < 1 || c.HistoryLimit > 10000 {
		return fmt.Errorf("historyLimit 必须在 1-10000 之间，当前 %d", c.HistoryLimit)
	}
	if c.ItemMaxBytes < 1024 || c.ItemMaxBytes > 16<<20 {
		return fmt.Errorf("itemMaxBytes 必须在 1024-16777216 之间，当前 %d", c.ItemMaxBytes)
	}
	if c.PollIntervalMs < 100 || c.PollIntervalMs > 5000 {
		return fmt.Errorf("pollIntervalMs 必须在 100-5000 之间，当前 %d", c.PollIntervalMs)
	}
	if strings.TrimSpace(c.RoomKey) == "" {
		return fmt.Errorf("roomKey 不能为空")
	}
	for _, s := range c.Seeds {
		if _, _, err := SplitHostPort(s); err != nil {
			return err
		}
	}
	return nil
}

// SplitHostPort 校验 "host:port" 形式的 seed 地址。
func SplitHostPort(seed string) (string, int, error) {
	idx := strings.LastIndex(seed, ":")
	if idx <= 0 || idx == len(seed)-1 {
		return "", 0, fmt.Errorf("seeds 里的地址必须形如 192.168.1.7:47100，当前 %q", seed)
	}
	port, err := strconv.Atoi(seed[idx+1:])
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("seeds 里的端口非法: %q", seed)
	}
	return seed[:idx], port, nil
}

// BaseURL 由 seed 地址构造 HTTP 基址。
func BaseURL(seed string) string {
	if strings.HasPrefix(seed, "http://") || strings.HasPrefix(seed, "https://") {
		return strings.TrimSuffix(seed, "/")
	}
	return "http://" + seed
}

// RoomHashValue 返回房间标识。密钥本身永不出网，只发出它的 hash。
func (c *Config) RoomHashValue() string { return model.RoomHash(c.RoomKey) }

// NewRoomKey 生成随机房间码。
func NewRoomKey() string {
	buf := make([]byte, roomKeyLen)
	if _, err := rand.Read(buf); err != nil {
		return "defaultroom"
	}
	out := make([]byte, roomKeyLen)
	for i, b := range buf {
		out[i] = roomKeyAlphabet[int(b)%len(roomKeyAlphabet)]
	}
	return string(out)
}

func newDeviceID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buf)
}
