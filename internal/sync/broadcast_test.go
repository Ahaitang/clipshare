package sync

import (
	"net/http"
	"net/http/httptest"
	stdsync "sync"
	"testing"
	"time"

	"clipshare/internal/history"
	"clipshare/internal/model"
)

// TestBroadcastIsolatesPeerFailure：三个 peer 里一个正常、一个 500、一个连不上，
// 正常的那个必须照常收到内容，另外两个只影响自己的 Result。
func TestBroadcastIsolatesPeerFailure(t *testing.T) {
	// 正常 peer：用真实 handler，验证端到端确实收下条目。
	store := history.New(10, "", false)
	var mu stdsync.Mutex
	var applied []model.Entry
	okSrv := NewServer(ServerConfig{
		RoomKey:  testRoomKey,
		NodeID:   "node-b",
		NodeName: "NODE-B",
		Version:  "0.1.0",
	}, store, func(e model.Entry) {
		mu.Lock()
		applied = append(applied, e)
		mu.Unlock()
	})
	okHTTP := httptest.NewServer(okSrv.Handler())
	defer okHTTP.Close()

	// 500 peer
	badHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer badHTTP.Close()

	// 拒绝连接 peer：起一个再关掉，拿到必然连不上的地址。
	deadHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadHTTP.URL
	deadHTTP.Close()

	peers := []Peer{
		{NodeID: "node-b", Name: "NODE-B", BaseURL: okHTTP.URL},
		{NodeID: "node-c", Name: "NODE-C", BaseURL: badHTTP.URL},
		{NodeID: "node-d", Name: "NODE-D", BaseURL: deadURL},
	}
	b := NewBroadcaster(&Client{Timeout: 2 * time.Second}, func() []Peer { return peers })
	b.SetRoomKey(testRoomKey)

	results := b.Broadcast(model.NewEntry("node-a", "NODE-A", "hello", time.Now()))
	if len(results) != len(peers) {
		t.Fatalf("结果数 = %d，期望 %d", len(results), len(peers))
	}
	if !results[0].OK() {
		t.Fatalf("正常 peer 应成功，实际 %v", results[0].Err)
	}
	if results[0].Peer.NodeID != "node-b" || results[0].Status != http.StatusAccepted {
		t.Fatalf("正常 peer 的结果不对: %+v", results[0])
	}
	if results[1].OK() || results[1].Status != http.StatusInternalServerError {
		t.Fatalf("500 peer 结果不对: %+v", results[1])
	}
	if results[2].OK() || results[2].Status != 0 {
		t.Fatalf("连不上的 peer 结果不对: %+v", results[2])
	}

	// 单个失败不影响正常 peer：内容确实落到了对端历史里。
	mu.Lock()
	got := append([]model.Entry(nil), applied...)
	mu.Unlock()
	if len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("正常 peer 收到 %+v，期望 1 条 hello", got)
	}
	if store.Count() != 1 {
		t.Fatalf("正常 peer 历史条数 = %d", store.Count())
	}

	failures := Failures(results)
	if len(failures) != 2 {
		t.Fatalf("失败结果数 = %d，期望 2", len(failures))
	}
}

// TestBroadcastNoPeers：没有 peer 时返回 nil，不 panic。
func TestBroadcastNoPeers(t *testing.T) {
	b := NewBroadcaster(&Client{}, nil)
	if got := b.Broadcast(testEntry("x")); got != nil {
		t.Fatalf("期望 nil，实际 %v", got)
	}
	b.SetRoomKey(testRoomKey)
	if got := b.Broadcast(testEntry("x")); got != nil {
		t.Fatalf("期望 nil，实际 %v", got)
	}
}

// TestBroadcastWithoutRoomKey：没设置 roomKey 时每个 peer 都失败并说明原因，
// 不能静默发出一个注定被 401 的请求。
func TestBroadcastWithoutRoomKey(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	b := NewBroadcaster(&Client{}, func() []Peer {
		return []Peer{{NodeID: "node-b", Name: "NODE-B", BaseURL: srv.URL}}
	})
	results := b.Broadcast(testEntry("x"))
	if len(results) != 1 || results[0].OK() {
		t.Fatalf("期望 1 条失败结果，实际 %+v", results)
	}
	if hits != 0 {
		t.Fatalf("roomKey 缺失时不应真的发出请求，实际命中 %d 次", hits)
	}
}

// TestBroadcastConcurrent：并发广播不串数据，所有 peer 每次都应收到。
func TestBroadcastConcurrent(t *testing.T) {
	const peers = 3
	const rounds = 5

	var mu stdsync.Mutex
	counts := make([]int, peers)
	servers := make([]*httptest.Server, 0, peers)
	for i := 0; i < peers; i++ {
		idx := i
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			counts[idx]++
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		}))
		servers = append(servers, srv)
	}
	defer func() {
		for _, s := range servers {
			s.Close()
		}
	}()

	peerList := make([]Peer, 0, peers)
	for i, s := range servers {
		peerList = append(peerList, Peer{NodeID: "node-" + string(rune('b'+i)), BaseURL: s.URL})
	}
	b := NewBroadcaster(&Client{}, func() []Peer { return peerList })
	b.SetRoomKey(testRoomKey)

	var wg stdsync.WaitGroup
	errs := make(chan string, rounds)
	for i := 0; i < rounds; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results := b.Broadcast(model.NewEntry("node-a", "NODE-A", "round-"+string(rune('0'+i)), time.Now()))
			for _, r := range results {
				if !r.OK() {
					errs <- r.Peer.BaseURL + ": " + r.Err.Error()
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("并发广播失败: %s", msg)
	}

	mu.Lock()
	defer mu.Unlock()
	for i, c := range counts {
		if c != rounds {
			t.Fatalf("peer %d 收到 %d 次，期望 %d 次", i, c, rounds)
		}
	}
}
