package discovery

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"
)

// serviceInfo 是 mDNS ServiceEntry 中与本包相关的字段。
// 抽成独立结构体，让过滤逻辑可以用纯函数单测（不需要真起 mDNS）。
type serviceInfo struct {
	Text     []string
	HostName string
	Port     int
	AddrIPv4 []net.IP
	AddrIPv6 []net.IP
}

// txtMap 把 ["room=abc","node=xyz"] 解析成 map；没有 '=' 的项忽略，
// 同名键以最后一次出现为准。
func txtMap(text []string) map[string]string {
	out := make(map[string]string, len(text))
	for _, kv := range text {
		i := strings.Index(kv, "=")
		if i <= 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}

// parseTXT 解析 design §7 约定的 TXT 记录：
// room=<roomHash>/node=<deviceId>/name=<nodeName>/ver=1。
// room 与 node 缺一不可，否则视为无效公告。
func parseTXT(text []string) (room, node, name, ver string) {
	m := txtMap(text)
	return m["room"], m["node"], m["name"], m["ver"]
}

// peer 从一条 mDNS 公告提取 Peer。
//
// 过滤规则（design §7）：只接受 room 与自身一致的实例、排除自身 NodeID、
// 排除无地址/无端口的无效公告。地址优先 IPv4，其次 IPv6，最后回落到主机名。
func (si serviceInfo) peer(selfRoom, selfNode string) (Peer, bool) {
	room, node, name, _ := parseTXT(si.Text)
	if room == "" || room != selfRoom {
		return Peer{}, false
	}
	if node == "" || node == selfNode {
		return Peer{}, false
	}
	host := firstUsableIP(si.AddrIPv4)
	if host == "" {
		host = firstUsableIP(si.AddrIPv6)
	}
	if host == "" {
		// 只去掉 DNS 末尾的点；保留 .local 后缀，否则主机名不可解析。
		host = strings.TrimSuffix(strings.TrimSpace(si.HostName), ".")
	}
	base := addrURL(host, si.Port)
	if base == "" {
		return Peer{}, false
	}
	if name == "" {
		name = shortID(node)
	}
	return Peer{NodeID: node, Name: name, BaseURL: base}, true
}

// firstUsableIP 跳过回环与未指定地址——公告里带 127.0.0.1 对远端毫无用处。
func firstUsableIP(ips []net.IP) string {
	for _, ip := range ips {
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		return ip.String()
	}
	return ""
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[:6]
}

// startMDNS 公告自身并订阅同房间实例。公告失败时直接返回错误，
// 由 Start 决定降级（seeds 仍可用）。
func (f *Finder) startMDNS(ctx context.Context) error {
	txt := []string{
		"room=" + f.cfg.RoomHash,
		"node=" + f.cfg.NodeID,
		"name=" + f.cfg.NodeName,
		"ver=1",
	}
	instance := f.cfg.NodeName
	if instance == "" {
		instance = shortID(f.cfg.NodeID)
	}
	instance = instance + "-" + shortID(f.cfg.NodeID)
	reg, err := zeroconf.Register(instance, serviceType, domain, f.cfg.Port, txt, nil)
	if err != nil {
		return err
	}
	f.reg = reg

	f.wg.Add(2)
	go f.browseLoop(ctx)
	go f.sweepLoop(ctx)
	return nil
}

// browseLoop 订阅服务并持续投喂条目。
//
// zeroconf 的 Browse 只做一次查询就返回，条目由它内部的 mainloop 在后台推送，
// 并且 mainloop 在自己的 ctx 结束时 **自行 close(entries)**。因此这里绝不能再 close
// 该通道（会 panic: close of closed channel / send on closed channel），
// 只负责消费它、并在订阅意外结束时重建一次。
func (f *Finder) browseLoop(ctx context.Context) {
	defer f.wg.Done()
	for {
		if ctx.Err() != nil {
			return
		}
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			f.setErr(err)
			f.logf("[discovery] 创建 mDNS resolver 失败: %v", err)
			if !sleepCtx(ctx, mdnsRetryDelay) {
				return
			}
			continue
		}

		entries := make(chan *zeroconf.ServiceEntry, 16)
		consumed := make(chan struct{})
		go func() {
			defer close(consumed)
			for e := range entries {
				f.onMDNSEntry(e)
			}
		}()

		if err := resolver.Browse(ctx, serviceType, domain, entries); err != nil {
			f.setErr(err)
			f.logf("[discovery] mDNS 订阅失败，%s 后重试: %v", mdnsRetryDelay, err)
			// Browse 失败时它内部已 cancel，mainloop 会 close(entries)，
			// consumer 自行退出；这里只等重试窗口。
			if ctx.Err() != nil {
				<-consumed
				return
			}
			if !sleepCtx(ctx, mdnsRetryDelay) {
				<-consumed
				return
			}
			continue
		}

		// 订阅已建立：等到 ctx 取消（正常退出）或通道被库内部关闭（需要重订）。
		select {
		case <-ctx.Done():
			<-consumed
			return
		case <-consumed:
			if ctx.Err() != nil {
				return
			}
			f.logf("[discovery] mDNS 订阅通道被关闭，%s 后重订", mdnsRetryDelay)
			if !sleepCtx(ctx, mdnsRetryDelay) {
				return
			}
			continue
		}
	}
}

// onMDNSEntry 记录一条公告并刷新过期时间。
func (f *Finder) onMDNSEntry(e *zeroconf.ServiceEntry) {
	if e == nil {
		return
	}
	si := serviceInfo{
		Text:     e.Text,
		HostName: e.HostName,
		Port:     e.Port,
		AddrIPv4: e.AddrIPv4,
		AddrIPv6: e.AddrIPv6,
	}
	peer, ok := si.peer(f.cfg.RoomHash, f.cfg.NodeID)
	if !ok {
		return
	}
	ttl := mdnsTTL
	if e.TTL > 0 {
		ttl = time.Duration(e.TTL) * time.Second
	}
	f.mu.Lock()
	f.mdnsPeers[peer.NodeID] = peer
	f.mdnsExp[peer.NodeID] = f.now().Add(ttl)
	f.mu.Unlock()
	f.publish()
}

// sweepLoop 周期性回收过期 peer。
func (f *Finder) sweepLoop(ctx context.Context) {
	defer f.wg.Done()
	t := time.NewTicker(mdnsResweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.sweepMDNS()
		}
	}
}
