package rpccontext

import (
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
)

// GetBlockDAGInfoCache is a small TTL cache for the GetBlockDAGInfo RPC response.
// It is intentionally conservative (short-lived) since the underlying values change frequently.
type GetBlockDAGInfoCache struct {
	mu        sync.RWMutex
	response  *appmessage.GetBlockDAGInfoResponseMessage
	expiresAt time.Time
}

func (c *GetBlockDAGInfoCache) Get(now time.Time) (*appmessage.GetBlockDAGInfoResponseMessage, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.response == nil || now.After(c.expiresAt) {
		return nil, false
	}

	return cloneGetBlockDAGInfoResponse(c.response), true
}

func (c *GetBlockDAGInfoCache) Set(response *appmessage.GetBlockDAGInfoResponseMessage, ttl time.Duration, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.response = cloneGetBlockDAGInfoResponse(response)
	c.expiresAt = now.Add(ttl)
}

func cloneGetBlockDAGInfoResponse(src *appmessage.GetBlockDAGInfoResponseMessage) *appmessage.GetBlockDAGInfoResponseMessage {
	if src == nil {
		return nil
	}

	dst := appmessage.NewGetBlockDAGInfoResponseMessage()
	dst.NetworkName = src.NetworkName
	dst.BlockCount = src.BlockCount
	dst.HeaderCount = src.HeaderCount
	if src.TipHashes != nil {
		dst.TipHashes = append([]string(nil), src.TipHashes...)
	}
	if src.VirtualParentHashes != nil {
		dst.VirtualParentHashes = append([]string(nil), src.VirtualParentHashes...)
	}
	dst.Difficulty = src.Difficulty
	dst.PastMedianTime = src.PastMedianTime
	dst.PruningPointHash = src.PruningPointHash
	dst.VirtualDAAScore = src.VirtualDAAScore
	dst.BlueScore = src.BlueScore
	dst.Error = src.Error

	return dst
}
