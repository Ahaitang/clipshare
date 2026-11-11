package discovery

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

func TestParseTXT(t *testing.T) {
	cases := []struct {
		name string
		text []string
		room string
		node string
		nm   string
		ver  string
	}{
		{
			name: "完整记录",
			text: []string{"room=abc123", "node=dev1", "name=MYPC", "ver=1"},
			room: "abc123", node: "dev1", nm: "MYPC", ver: "1",
		},
		{
			name: "顺序无关且含无关键",
			text: []string{"ver=1", "x=y", "node=dev2", "room=abc123"},
			room: "abc123", node: "dev2", ver: "1",
		},
		{
			name: "缺少等号的项被忽略",
			text: []string{"room=abc123", "garbage", "node=dev3"},
			room: "abc123", node: "dev3",
		},
		{
			name: "空值",
			text: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			room, node, nm, ver := parseTXT(tc.text)
			if room != tc.room || node != tc.node || nm != tc.nm || ver != tc.ver {
				t.Fatalf("parseTXT(%v) = (%q,%q,%q,%q)，期望 (%q,%q,%q,%q)",
					tc.text, room, node, nm, ver, tc.room, tc.node, tc.nm, tc.ver)
			}
		})
	}
}

func TestServiceInfoPeer(t *testing.T) {
	const (
		selfRoom = "room-abc"
		selfNode = "self"
	)
	base := []string{"room=room-abc", "node=peer1", "name=PEER", "ver=1"}

	t.Run("正常 IPv4 公告", func(t *testing.T) {
		si := serviceInfo{Text: base, Port: 47100, AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")}}
		p, ok := si.peer(selfRoom, selfNode)
		if !ok {
			t.Fatal("期望被接受")
		}
		if p.NodeID != "peer1" || p.Name != "PEER" || p.BaseURL != "http://192.168.1.7:47100" {
			t.Fatalf("异常结果: %+v", p)
		}
	})

	t.Run("room 不一致被拒", func(t *testing.T) {
		si := serviceInfo{
			Text:     []string{"room=other", "node=peer1", "name=PEER"},
			Port:     47100,
			AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")},
		}
		if _, ok := si.peer(selfRoom, selfNode); ok {
			t.Fatal("不同房间的实例必须被拒绝")
		}
	})

	t.Run("room 缺失被拒", func(t *testing.T) {
		si := serviceInfo{Text: []string{"node=peer1"}, Port: 47100, AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")}}
		if _, ok := si.peer(selfRoom, selfNode); ok {
			t.Fatal("缺 room 必须被拒绝")
		}
	})

	t.Run("自身被排除", func(t *testing.T) {
		si := serviceInfo{
			Text:     []string{"room=room-abc", "node=self", "name=ME"},
			Port:     47100,
			AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")},
		}
		if _, ok := si.peer(selfRoom, selfNode); ok {
			t.Fatal("自身公告必须被排除")
		}
	})

	t.Run("node 缺失被拒", func(t *testing.T) {
		si := serviceInfo{Text: []string{"room=room-abc"}, Port: 47100, AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")}}
		if _, ok := si.peer(selfRoom, selfNode); ok {
			t.Fatal("缺 node 必须被拒绝")
		}
	})

	t.Run("回环地址跳过，用 IPv6", func(t *testing.T) {
		si := serviceInfo{
			Text:     base,
			Port:     47100,
			AddrIPv4: []net.IP{net.ParseIP("127.0.0.1")},
			AddrIPv6: []net.IP{net.ParseIP("fe80::1")},
		}
		p, ok := si.peer(selfRoom, selfNode)
		if !ok {
			t.Fatal("期望被接受")
		}
		if p.BaseURL != "http://[fe80::1]:47100" {
			t.Fatalf("IPv6 BaseURL 异常: %q", p.BaseURL)
		}
	})

	t.Run("无 IP 时回落到主机名", func(t *testing.T) {
		si := serviceInfo{Text: base, Port: 47100, HostName: "other-pc.local."}
		p, ok := si.peer(selfRoom, selfNode)
		if !ok {
			t.Fatal("期望被接受")
		}
		if p.BaseURL != "http://other-pc.local:47100" {
			t.Fatalf("主机名 BaseURL 异常: %q", p.BaseURL)
		}
	})

	t.Run("端口非法被拒", func(t *testing.T) {
		si := serviceInfo{Text: base, Port: 0, AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")}}
		if _, ok := si.peer(selfRoom, selfNode); ok {
			t.Fatal("端口非法必须被拒绝")
		}
	})

	t.Run("name 缺失时用 node 前缀", func(t *testing.T) {
		si := serviceInfo{
			Text:     []string{"room=room-abc", "node=abcdef123456"},
			Port:     47100,
			AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")},
		}
		p, ok := si.peer(selfRoom, selfNode)
		if !ok {
			t.Fatal("期望被接受")
		}
		if p.Name != "abcdef" {
			t.Fatalf("兜底名称异常: %q", p.Name)
		}
	})
}

func TestHealthPeer(t *testing.T) {
	const seed = "192.168.1.9:47100"
	h := healthInfo{NodeID: "peer9", Name: "PC9", RoomHash: "room-abc", Version: "1"}

	if p, ok := healthPeer(h, seed, "room-abc", "self"); !ok || p.BaseURL != "http://192.168.1.9:47100" {
		t.Fatalf("同房间 seed 应被收编，得到 %+v ok=%v", p, ok)
	}
	if _, ok := healthPeer(h, seed, "other-room", "self"); ok {
		t.Fatal("roomHash 不一致必须被拒绝")
	}
	if _, ok := healthPeer(h, seed, "room-abc", "peer9"); ok {
		t.Fatal("seeds 填了自己时必须被过滤")
	}
	if _, ok := healthPeer(healthInfo{RoomHash: "room-abc"}, seed, "room-abc", "self"); ok {
		t.Fatal("缺 nodeId 必须被拒绝")
	}
	if _, ok := healthPeer(healthInfo{NodeID: "peer9"}, seed, "room-abc", "self"); ok {
		t.Fatal("缺 roomHash 必须被拒绝")
	}
}

func TestSamePeersStable(t *testing.T) {
	a := []Peer{{NodeID: "1", Name: "A", BaseURL: "http://x:1"}}
	b := []Peer{{NodeID: "1", Name: "A", BaseURL: "http://x:1"}}
	if !samePeers(a, b) {
		t.Fatal("内容相同应判定相等")
	}
	b[0].BaseURL = "http://y:1"
	if samePeers(a, b) {
		t.Fatal("内容不同应判定不等")
	}
	if samePeers(nil, []Peer{}) != true {
		t.Fatal("nil 与空切片应视为相等")
	}
}

// healthServer 起一个返回指定 health JSON 的假节点。
func healthServer(t *testing.T, h healthInfo) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func seedOf(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

func newSilentFinder(cfg Config, onUpdate func([]Peer)) *Finder {
	f := New(cfg, onUpdate)
	f.SetLogger(nil)
	return f
}

func TestProbeSeeds(t *testing.T) {
	srv := healthServer(t, healthInfo{NodeID: "peer9", Name: "PC9", RoomHash: "room-abc"})
	self := healthServer(t, healthInfo{NodeID: "self", Name: "ME", RoomHash: "room-abc"})
	other := healthServer(t, healthInfo{NodeID: "peerX", Name: "PX", RoomHash: "other-room"})

	f := newSilentFinder(Config{
		NodeID:   "self",
		RoomHash: "room-abc",
		Seeds:    []string{seedOf(srv), seedOf(self), seedOf(other)},
	}, nil)

	f.probeSeeds(context.Background())

	peers := f.Peers()
	if len(peers) != 1 {
		t.Fatalf("期望只剩 1 个同房间 peer，得到 %d: %+v", len(peers), peers)
	}
	if peers[0].NodeID != "peer9" || peers[0].BaseURL != "http://"+seedOf(srv) {
		t.Fatalf("收编结果异常: %+v", peers[0])
	}
	if err := f.LastError(); err != nil {
		t.Fatalf("有 peer 时不记录错误，得到 %v", err)
	}
}

// 一个 seed 挂掉不应污染状态行；全挂才记录错误，恢复后清空。
func TestProbeSeedsErrorOnlyWhenAllFail(t *testing.T) {
	good := healthServer(t, healthInfo{NodeID: "peer9", RoomHash: "room-abc"})
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := seedOf(dead)
	dead.Close() // 端口立即拒绝连接

	f := newSilentFinder(Config{
		NodeID:   "self",
		RoomHash: "room-abc",
		Seeds:    []string{deadURL, seedOf(good)},
	}, nil)

	f.probeSeeds(context.Background())
	if err := f.LastError(); err != nil {
		t.Fatalf("部分 seed 可用时不应记录错误，得到 %v", err)
	}
	if len(f.Peers()) != 1 {
		t.Fatalf("期望收编 1 个 peer，得到 %+v", f.Peers())
	}

	onlyDead := newSilentFinder(Config{
		NodeID:   "self",
		RoomHash: "room-abc",
		Seeds:    []string{deadURL},
	}, nil)
	onlyDead.probeSeeds(context.Background())
	if err := onlyDead.LastError(); err == nil {
		t.Fatal("全部 seed 不可达时应记录错误")
	}
	if len(onlyDead.Peers()) != 0 {
		t.Fatalf("不可达时不应有 peer，得到 %+v", onlyDead.Peers())
	}
}

// seeds 下线后应从 peer 列表移除，而不是永久滞留。
func TestProbeSeedsRemovesOffline(t *testing.T) {
	h := healthInfo{NodeID: "peer9", RoomHash: "room-abc"}
	srv := healthServer(t, h)
	f := newSilentFinder(Config{NodeID: "self", RoomHash: "room-abc", Seeds: []string{seedOf(srv)}}, nil)
	f.probeSeeds(context.Background())
	if len(f.Peers()) != 1 {
		t.Fatalf("首轮应收编 1 个 peer，得到 %+v", f.Peers())
	}

	f.cfg.Seeds = nil // 模拟 seed 配置下线/移除
	f.probeSeeds(context.Background())
	if len(f.Peers()) != 0 {
		t.Fatalf("下线后应清空，得到 %+v", f.Peers())
	}
}

// MDNS=false 时仅靠 seeds 也要真的能发现（禁多播网络的唯一路径）。
func TestFinderSeedsOnly(t *testing.T) {
	srv := healthServer(t, healthInfo{NodeID: "peer9", Name: "PC9", RoomHash: "room-abc"})

	updates := make(chan []Peer, 4)
	f := newSilentFinder(Config{
		NodeID:   "self",
		NodeName: "ME",
		RoomHash: "room-abc",
		Seeds:    []string{seedOf(srv)},
		MDNS:     false,
	}, func(peers []Peer) { updates <- peers })

	if err := f.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	defer f.Stop()

	select {
	case peers := <-updates:
		if len(peers) != 1 || peers[0].NodeID != "peer9" {
			t.Fatalf("期望收到 1 个 peer，得到 %+v", peers)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("5s 内没有收到 peer 更新")
	}

	// Start 后集合不变，不应再重复回调。
	select {
	case peers := <-updates:
		t.Fatalf("集合未变化却重复回调: %+v", peers)
	case <-time.After(300 * time.Millisecond):
	}
}

// MDNS=false 且无 seeds 时应能安全启动/停止（不注册 mDNS，不起 goroutine）。
func TestFinderNoSources(t *testing.T) {
	f := newSilentFinder(Config{NodeID: "self", RoomHash: "room-abc"}, nil)
	if err := f.Start(); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	f.Stop()
	f.Stop() // 重复 Stop 必须安全
	if len(f.Peers()) != 0 {
		t.Fatalf("无来源时不应有 peer，得到 %+v", f.Peers())
	}
}

// 同一节点同时由 mDNS 和 seeds 发现时按 deviceId 去重，mDNS 的地址优先。
func TestMergeDedupPrefersMDNS(t *testing.T) {
	f := newSilentFinder(Config{NodeID: "self", RoomHash: "room-abc"}, nil)
	f.mu.Lock()
	f.seedPeers = map[string]Peer{"peer1": {NodeID: "peer1", Name: "OLD", BaseURL: "http://10.0.0.1:47100"}}
	f.mdnsPeers = map[string]Peer{"peer1": {NodeID: "peer1", Name: "PC1", BaseURL: "http://192.168.1.7:47100"}}
	f.mu.Unlock()

	peers := f.Peers() // 尚未 publish，列表为空
	if len(peers) != 0 {
		t.Fatalf("publish 前 Peers 应为空，得到 %+v", peers)
	}
	f.publish()
	peers = f.Peers()
	if len(peers) != 1 {
		t.Fatalf("去重后应只有 1 个 peer，得到 %+v", peers)
	}
	if peers[0].BaseURL != "http://192.168.1.7:47100" {
		t.Fatalf("mDNS 地址应优先，得到 %+v", peers[0])
	}
}

// mDNS peer 在 TTL 过期后被回收（zeroconf v1 不提供 goodbye 回调）。
func TestMDNSEntryExpiry(t *testing.T) {
	now := time.Now()
	var published atomic.Int64
	f := newSilentFinder(Config{NodeID: "self", NodeName: "ME", RoomHash: "room-abc", Port: 47100},
		func(peers []Peer) { published.Store(int64(len(peers))) })
	f.now = func() time.Time { return now }

	f.onMDNSEntry(&zeroconf.ServiceEntry{
		ServiceRecord: zeroconf.ServiceRecord{Instance: "PC1"},
		HostName:      "pc1.local.",
		Port:          47100,
		Text:          []string{"room=room-abc", "node=peer1", "name=PC1", "ver=1"},
		AddrIPv4:      []net.IP{net.ParseIP("192.168.1.7")},
		TTL:           120,
	})
	if len(f.Peers()) != 1 {
		t.Fatalf("应收录 1 个 mDNS peer，得到 %+v", f.Peers())
	}

	// 时间推进到 TTL 之后，但仍接受新的公告前，应被回收。
	now = now.Add(121 * time.Second)
	f.sweepMDNS()
	if len(f.Peers()) != 0 {
		t.Fatalf("过期后应回收，得到 %+v", f.Peers())
	}
	if published.Load() != 0 {
		t.Fatalf("回收后应回调空集合，最后一次回调计数 %d", published.Load())
	}
}

// 不同房间的 mDNS 公告不应影响 peer 集合。
func TestMDNSEntryRejectsOtherRoom(t *testing.T) {
	f := newSilentFinder(Config{NodeID: "self", NodeName: "ME", RoomHash: "room-abc", Port: 47100}, nil)
	f.onMDNSEntry(&zeroconf.ServiceEntry{
		Port:     47100,
		Text:     []string{"room=other", "node=peer1", "name=PC1"},
		AddrIPv4: []net.IP{net.ParseIP("192.168.1.7")},
		TTL:      120,
	})
	if len(f.Peers()) != 0 {
		t.Fatalf("不同房间的公告必须被忽略，得到 %+v", f.Peers())
	}
}

func TestAddrURL(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"192.168.1.7", 47100, "http://192.168.1.7:47100"},
		{"::1", 47100, "http://[::1]:47100"},
		{"", 47100, ""},
		{"192.168.1.7", 0, ""},
		{"192.168.1.7", 70000, ""},
	}
	for _, tc := range cases {
		if got := addrURL(tc.host, tc.port); got != tc.want {
			t.Fatalf("addrURL(%q,%d) = %q，期望 %q", tc.host, tc.port, got, tc.want)
		}
	}
}
