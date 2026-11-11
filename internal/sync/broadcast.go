package sync

import (
	"context"
	stdsync "sync"

	"clipshare/internal/model"
)

// Result 是一条广播结果，每个 peer 一个。
type Result struct {
	Peer Peer
	// Status 是 HTTP 状态码：成功时为对端返回的 2xx；失败且拿到响应时为错误码；
	// 0 表示没拿到响应（连不上、超时）。
	Status int
	// Err 为 nil 表示对端已接受（2xx）。
	Err error
}

// OK 报告该 peer 是否接收成功。
func (r Result) OK() bool { return r.Err == nil }

// Failures 返回所有失败的广播结果，便于调用方一次记日志。
func Failures(results []Result) []Result {
	var out []Result
	for _, r := range results {
		if r.Err != nil {
			out = append(out, r)
		}
	}
	return out
}

// Broadcaster 把一条条目扇出到当前所有 peers。
//
// peers 用回调提供，这样 discovery 更新 peer 集合时不需要重建 Broadcaster。
type Broadcaster struct {
	client *Client
	peers  func() []Peer

	mu      stdsync.RWMutex
	roomKey string
}

// NewBroadcaster 组装广播器。c 为 nil 时用零值 Client（默认 3s 超时）。
func NewBroadcaster(c *Client, peers func() []Peer) *Broadcaster {
	if c == nil {
		c = &Client{}
	}
	if peers == nil {
		peers = func() []Peer { return nil }
	}
	return &Broadcaster{client: c, peers: peers}
}

// SetRoomKey 设置房间密钥，Broadcast 时用于构造 Bearer 头与 X-Room。
func (b *Broadcaster) SetRoomKey(roomKey string) {
	b.mu.Lock()
	b.roomKey = roomKey
	b.mu.Unlock()
}

// RoomKey 返回当前房间密钥。
func (b *Broadcaster) RoomKey() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.roomKey
}

// Broadcast 向每个 peer 各起一个 goroutine 并发发送，等全部返回后汇总。
//
// 任一 peer 失败不影响其他 peer（spec §6），也不重试（S7）。
// 结果顺序与 peers() 返回顺序一致，方便与 peer 列表对齐。
func (b *Broadcaster) Broadcast(e model.Entry) []Result {
	peers := b.peers()
	if len(peers) == 0 {
		return nil
	}
	roomKey := b.RoomKey()

	results := make([]Result, len(peers))
	var wg stdsync.WaitGroup
	for i := range peers {
		wg.Add(1)
		go func(i int, p Peer) {
			defer wg.Done()
			status, err := b.client.post(context.Background(), p, roomKey, e)
			results[i] = Result{Peer: p, Status: status, Err: err}
		}(i, peers[i])
	}
	wg.Wait()
	return results
}
