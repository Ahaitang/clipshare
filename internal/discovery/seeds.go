package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"clipshare/internal/config"
)

// healthInfo 是 GET /v1/health 的响应（design §6）。
type healthInfo struct {
	NodeID   string `json:"nodeId"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	RoomHash string `json:"roomHash"`
	Clients  int    `json:"clients"`
}

// healthPeer 由健康检查响应判断该 seed 是否可收编：
// roomHash 必须与自身一致（防误配密钥串房间），且 nodeId ≠ 自身（seeds 里填了自己）。
func healthPeer(h healthInfo, seed, selfRoom, selfNode string) (Peer, bool) {
	if h.NodeID == "" || h.NodeID == selfNode {
		return Peer{}, false
	}
	if h.RoomHash == "" || h.RoomHash != selfRoom {
		return Peer{}, false
	}
	base := config.BaseURL(seed)
	name := h.Name
	if name == "" {
		name = shortID(h.NodeID)
	}
	return Peer{NodeID: h.NodeID, Name: name, BaseURL: base}, true
}

// probeSeed 对一个静态 seed 做 /v1/health 探活。
// 返回 ok=false 且 err=nil 表示「连上了但不是同房间/就是自己」，属正常情况。
func (f *Finder) probeSeed(ctx context.Context, seed string) (Peer, bool, error) {
	base := config.BaseURL(seed)
	ctx, cancel := context.WithTimeout(ctx, seedProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/health", nil)
	if err != nil {
		return Peer{}, false, fmt.Errorf("seed %q 地址非法: %w", seed, err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return Peer{}, false, fmt.Errorf("seed %q 探活失败: %w", seed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return Peer{}, false, fmt.Errorf("seed %q 健康检查返回 %s", seed, resp.Status)
	}
	var h healthInfo
	dec := json.NewDecoder(io.LimitReader(resp.Body, 64<<10))
	if err := dec.Decode(&h); err != nil {
		return Peer{}, false, fmt.Errorf("seed %q 健康检查响应无法解析: %w", seed, err)
	}
	peer, ok := healthPeer(h, seed, f.cfg.RoomHash, f.cfg.NodeID)
	return peer, ok, nil
}

// probeSeeds 并发探测所有 seeds，并用本轮结果整体替换 seeds 一侧的 peer 集合
// （已下线的 seed 不会永久滞留在列表里）。
//
// 错误处理：只有当本轮一条都没探活成功时才记录 lastErr——单个 seed 挂掉是常态，
// 不应该让托盘状态行一直显示错误；本轮恢复正常后清掉旧错误。
func (f *Finder) probeSeeds(ctx context.Context) {
	found := make(map[string]Peer, len(f.cfg.Seeds))
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		firstErr error
	)
	for _, seed := range f.cfg.Seeds {
		seed := seed
		wg.Add(1)
		go func() {
			defer wg.Done()
			peer, ok, err := f.probeSeed(ctx, seed)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if !ok {
				return
			}
			mu.Lock()
			found[peer.NodeID] = peer
			mu.Unlock()
		}()
	}
	wg.Wait()

	f.mu.Lock()
	f.seedPeers = found
	if len(found) > 0 {
		f.lastErr = nil
	} else if firstErr != nil {
		f.lastErr = firstErr
	}
	f.mu.Unlock()
	f.publish()
}
