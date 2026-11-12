// Package onboard 处理「把一台从未配置过的电脑接进房间」这件事。
//
// 原本要手工做两件事：把 12 位房间码抄过去、以及在禁多播的网络上手填 seeds。
// 这里把它们合成一条可以直接复制粘贴的「邀请码」：
//
//	clipshare1:<roomKey>@<host:port>[,<host:port>...]
//
// 设计取舍：
//   - 明文可读、可手抄，不搞 base64——用户会把它发到聊天窗口里，可读才好排错；
//   - 带版本前缀 clipshare1:，将来格式变了能明确报错而不是瞎猜；
//   - 只包含「加入房间必需」的信息，不含任何设备标识。
package onboard

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"clipshare/internal/config"
)

// Prefix 是邀请码的版本前缀。
const Prefix = "clipshare1:"

// Invite 是一条邀请码的内容。
type Invite struct {
	RoomKey string
	Seeds   []string // 形如 host:port；可以为空（同网段且允许多播时不需要）
}

// String 把邀请码渲染成可复制的文本。
func (i Invite) String() string {
	body := i.RoomKey
	if len(i.Seeds) > 0 {
		body += "@" + strings.Join(i.Seeds, ",")
	}
	return Prefix + body
}

// Parse 解析邀请码。允许两侧空白与整段粘贴中的多余空格。
func Parse(code string) (Invite, error) {
	raw := strings.TrimSpace(code)
	if raw == "" {
		return Invite{}, fmt.Errorf("邀请码为空")
	}
	if !strings.HasPrefix(strings.ToLower(raw), Prefix) {
		return Invite{}, fmt.Errorf("不是有效的邀请码（应以 %s 开头）", Prefix)
	}
	body := strings.TrimSpace(raw[len(Prefix):])

	room, seedsPart, hasSeeds := strings.Cut(body, "@")
	room = strings.TrimSpace(room)
	if room == "" {
		return Invite{}, fmt.Errorf("邀请码里缺少房间码")
	}
	if strings.ContainsAny(room, " \t\r\n") {
		return Invite{}, fmt.Errorf("房间码不能包含空白字符")
	}

	inv := Invite{RoomKey: room}
	if hasSeeds {
		for _, s := range strings.Split(seedsPart, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			host, portStr, err := net.SplitHostPort(s)
			if err != nil || strings.TrimSpace(host) == "" {
				return Invite{}, fmt.Errorf("邀请码里的地址 %q 不是 host:port 形式", s)
			}
			port, err := strconv.Atoi(portStr)
			if err != nil || port < 1 || port > 65535 {
				return Invite{}, fmt.Errorf("邀请码里的端口非法: %q", s)
			}
			inv.Seeds = append(inv.Seeds, s)
		}
	}
	return inv, nil
}

// Build 根据配置生成本机的邀请码：房间码 + 本机局域网地址与端口。
//
// 地址优先取「到已有 peer 的出口网卡」——多网卡机器上「第一个私有 IP」经常不是
// 对方能连到的那个（本机就有 172.24/172.26 两段，实测挑错导致邀请码直接不可用）。
//
// 探测不到地址时退化为「只有房间码」：那种情况下对方仍可通过 mDNS
// 或手动填 seeds 加入，邀请码不该因此失效。
func Build(cfg *config.Config) Invite {
	inv := Invite{RoomKey: cfg.RoomKey}
	if cfg.Port <= 0 {
		return inv
	}
	ip := egressIP(cfg.Seeds)
	if ip == "" {
		ip = LANIP()
	}
	if ip != "" {
		inv.Seeds = []string{net.JoinHostPort(ip, strconv.Itoa(cfg.Port))}
	}
	return inv
}

// egressIP 返回「本机到指定 peer 会走哪张网卡」的本地地址。
//
// 用 UDP Dial 探测：不实际发包，只让内核做路由选择，因此无需对端在线。
func egressIP(seeds []string) string {
	for _, seed := range seeds {
		host, port, err := net.SplitHostPort(seed)
		if err != nil || host == "" {
			continue
		}
		conn, err := net.Dial("udp", net.JoinHostPort(host, port))
		if err != nil {
			continue
		}
		addr, ok := conn.LocalAddr().(*net.UDPAddr)
		_ = conn.Close()
		if ok && addr.IP != nil && !addr.IP.IsLoopback() {
			return addr.IP.String()
		}
	}
	return ""
}

// LANIP 返回本机的一个私有网段 IPv4 地址（用于让对方主动连过来）。
//
// 只挑私有地址：公网地址对局域网内互连没意义，而回环地址对方连不上。
func LANIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var fallback string
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		ip4 := ipNet.IP.To4()
		if ip4 == nil {
			continue
		}
		if ip4.IsPrivate() {
			return ip4.String()
		}
		if fallback == "" {
			fallback = ip4.String()
		}
	}
	return fallback
}

// Apply 把邀请码写进配置（房间码 + seeds），按需落盘。
//
// seeds 采用「替换」语义：邀请码描述的就是应该连的那台机器，
// 追加会把历史遗留的错误地址留着，反而更难排查。
func Apply(cfg *config.Config, inv Invite) {
	cfg.RoomKey = inv.RoomKey
	if len(inv.Seeds) > 0 {
		cfg.Seeds = append([]string{}, inv.Seeds...)
	}
}
