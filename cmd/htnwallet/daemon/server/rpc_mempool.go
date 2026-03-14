package server

import (
	"context"
	"time"

	pb "github.com/Hoosat-Oy/HTND/cmd/htnwallet/daemon/pb"
)

// GetMempoolEntry returns whether the node's mempool (including orphan pool) currently contains txid.
func (s *server) GetMempoolEntry(ctx context.Context, req *pb.GetMempoolEntryRequest) (*pb.GetMempoolEntryResponse, error) {
	if req == nil || req.Txid == "" {
		return &pb.GetMempoolEntryResponse{
			Found:    false,
			IsOrphan: false,
			Error:    "missing txid",
		}, nil
	}

	// Query the node RPC for mempool entry (include orphan pool)
	resp, err := s.rpcClient.GetMempoolEntry(req.Txid, true, false)
	if err != nil {
		// Convert RPC error text into response.Error rather than returning gRPC error.
		return &pb.GetMempoolEntryResponse{Found: false, Error: err.Error()}, nil
	}

	if resp == nil || resp.Entry == nil {
		return &pb.GetMempoolEntryResponse{Found: false}, nil
	}

	return &pb.GetMempoolEntryResponse{Found: true, IsOrphan: resp.Entry.IsOrphan}, nil
}

// WaitForMempoolEntries waits up to timeout_seconds for all txids to become visible in the node's mempool (including orphan pool).
// It polls s.rpcClient.GetMempoolEntry periodically and returns when all observed or timeout elapses.
func (s *server) WaitForMempoolEntries(ctx context.Context, req *pb.WaitForMempoolEntriesRequest) (*pb.WaitForMempoolEntriesResponse, error) {
	if req == nil || len(req.Txids) == 0 {
		return &pb.WaitForMempoolEntriesResponse{Ok: true}, nil
	}

	// Default timeout if not supplied
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)

	observed := make(map[string]struct{}, len(req.Txids))

	// Helper to probe a single txid (returns true if present)
	probe := func(txid string) bool {
		resp, err := s.rpcClient.GetMempoolEntry(txid, true, false)
		if err != nil {
			return false
		}
		return resp != nil && resp.Entry != nil
	}

	// Initial quick probe
	for _, txid := range req.Txids {
		if probe(txid) {
			observed[txid] = struct{}{}
		}
	}

	// Short poll loop until all observed or timeout or context canceled
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		// If context done -> return partial result
		select {
		case <-ctx.Done():
			obs := make([]string, 0, len(observed))
			for txid := range observed {
				obs = append(obs, txid)
			}
			return &pb.WaitForMempoolEntriesResponse{Ok: false, ObservedTxids: obs, Error: ctx.Err().Error()}, nil
		default:
		}

		allObserved := true
		for _, txid := range req.Txids {
			if _, ok := observed[txid]; ok {
				continue
			}
			allObserved = false
			break
		}
		if allObserved {
			obs := make([]string, 0, len(observed))
			for txid := range observed {
				obs = append(obs, txid)
			}
			return &pb.WaitForMempoolEntriesResponse{Ok: true, ObservedTxids: obs}, nil
		}

		// check deadline
		if time.Now().After(deadline) {
			obs := make([]string, 0, len(observed))
			for txid := range observed {
				obs = append(obs, txid)
			}
			return &pb.WaitForMempoolEntriesResponse{Ok: false, ObservedTxids: obs}, nil
		}

		// Wait for next tick or context done
		select {
		case <-ticker.C:
			// probe missing txids
			for _, txid := range req.Txids {
				if _, ok := observed[txid]; ok {
					continue
				}
				if probe(txid) {
					observed[txid] = struct{}{}
				}
			}
		case <-ctx.Done():
			obs := make([]string, 0, len(observed))
			for txid := range observed {
				obs = append(obs, txid)
			}
			return &pb.WaitForMempoolEntriesResponse{Ok: false, ObservedTxids: obs, Error: ctx.Err().Error()}, nil
		}
	}
}
