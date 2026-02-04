package rpcspam

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/infrastructure/network/rpcclient"
	"github.com/Hoosat-Oy/HTND/stability-tests/common"
	"github.com/Hoosat-Oy/HTND/util/panics"
	"github.com/Hoosat-Oy/HTND/util/profiling"
)

type rpcCall func(client *rpcclient.RPCClient) error

func TestRPCSpam(t *testing.T) {
	if os.Getenv("RUN_STABILITY_TESTS") == "" {
		t.Skip()
	}

	defer panics.HandlePanic(log, "rpc-spam", nil)
	if err := parseConfig(); err != nil {
		t.Fatalf("parseConfig: %s", err)
	}
	defer backendLog.Close()
	common.UseLogger(backendLog, log.Level())

	cfg := activeConfig()
	if cfg.Profile != "" {
		profiling.Start(cfg.Profile, log)
	}

	if cfg.Workers <= 0 {
		t.Fatalf("--workers must be > 0")
	}
	if cfg.Clients <= 0 {
		t.Fatalf("--clients must be > 0")
	}

	// Optional env overrides (handy for CI / ad-hoc runs)
	if v := os.Getenv("RPC_SPAM_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Workers = n
		}
	}
	if v := os.Getenv("RPC_SPAM_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Duration = d
		}
	}

	var cleanup func()
	if cfg.StartNode {
		cleanup = common.RunHoosatdForTesting(t, "rpc-spam", cfg.RPCAddress)
		defer cleanup()
	}

	clients := make([]*rpcclient.RPCClient, 0, cfg.Clients)
	for i := 0; i < cfg.Clients; i++ {
		client, err := connectWithRetry(cfg.RPCAddress, 30*time.Second)
		if err != nil {
			t.Fatalf("connect to %s: %s", cfg.RPCAddress, err)
		}
		client.SetTimeout(cfg.PerCallTimeout)
		clients = append(clients, client)
		defer client.Close()
	}

	// Cache the selected tip hash for methods like GetBlock.
	// We refresh periodically so long-running runs keep hitting recent blocks.
	var selectedTipHash atomic.Value
	selectedTipHash.Store("")
	refreshSelectedTipHash := func() {
		resp, err := clients[0].GetSelectedTipHash()
		if err != nil {
			log.Warnf("rpc-spam: GetSelectedTipHash failed: %s", err)
			return
		}
		if resp.SelectedTipHash == "" {
			log.Warnf("rpc-spam: GetSelectedTipHash returned empty hash")
			return
		}
		selectedTipHash.Store(resp.SelectedTipHash)
	}
	refreshSelectedTipHash()
	getSelectedTipHash := func() string {
		v := selectedTipHash.Load()
		if v == nil {
			return ""
		}
		s, _ := v.(string)
		return s
	}

	calls, err := parseMethods(cfg.Methods, getSelectedTipHash)
	if err != nil {
		t.Fatalf("parse methods: %s", err)
	}
	if len(calls) == 0 {
		t.Fatalf("no methods configured")
	}

	deadlineCtx := context.Background()
	var cancel context.CancelFunc
	if cfg.TotalCalls == 0 {
		deadlineCtx, cancel = context.WithTimeout(deadlineCtx, cfg.Duration)
		defer cancel()
	}

	t.Logf("RPC spam starting: rpc=%s clients=%d workers=%d duration=%s calls=%d methods=%s",
		cfg.RPCAddress, cfg.Clients, cfg.Workers, cfg.Duration, cfg.TotalCalls, cfg.Methods)

	var totalRequests int64
	var totalErrors int64
	var totalLatencyNanoseconds int64
	var maxLatencyNanoseconds int64

	progressDone := make(chan struct{})
	if cfg.ProgressInterval > 0 {
		start := time.Now()
		go func() {
			ticker := time.NewTicker(cfg.ProgressInterval)
			defer ticker.Stop()
			lastRequests := int64(0)
			lastTick := start
			for {
				select {
				case <-progressDone:
					return
				case <-ticker.C:
					now := time.Now()
					req := atomic.LoadInt64(&totalRequests)
					errCount := atomic.LoadInt64(&totalErrors)
					latNs := atomic.LoadInt64(&totalLatencyNanoseconds)
					maxNs := atomic.LoadInt64(&maxLatencyNanoseconds)
					elapsed := now.Sub(start)
					if elapsed <= 0 {
						elapsed = time.Nanosecond
					}
					rps := float64(req) / elapsed.Seconds()
					interval := now.Sub(lastTick)
					if interval <= 0 {
						interval = time.Nanosecond
					}
					intervalRps := float64(req-lastRequests) / interval.Seconds()
					avgLatency := time.Duration(0)
					if req > 0 {
						avgLatency = time.Duration(latNs / req)
					}

					log.Infof("rpc-spam progress: elapsed=%s total=%d errors=%d rps=%.0f intervalRps=%.0f avg=%s max=%s",
						elapsed, req, errCount, rps, intervalRps, avgLatency, time.Duration(maxNs))

					lastRequests = req
					lastTick = now
				}
			}
		}()
	}

	// Refresh the selected tip hash on a slower cadence than the progress log.
	// This keeps GetBlock requests relevant without spamming GetSelectedTipHash.
	go func() {
		interval := 30 * time.Second
		if cfg.ProgressInterval > 0 && cfg.ProgressInterval < interval {
			interval = cfg.ProgressInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-progressDone:
				return
			case <-ticker.C:
				refreshSelectedTipHash()
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(cfg.Workers)

	start := time.Now()
	for workerID := 0; workerID < cfg.Workers; workerID++ {
		workerID := workerID
		go func() {
			defer wg.Done()
			client := clients[workerID%len(clients)]
			callIndex := 0

			for {
				if cfg.TotalCalls == 0 {
					select {
					case <-deadlineCtx.Done():
						return
					default:
					}
				}

				if cfg.TotalCalls > 0 {
					base := cfg.TotalCalls / cfg.Workers
					remainder := cfg.TotalCalls % cfg.Workers
					quota := base
					if workerID < remainder {
						quota++
					}
					if callIndex >= quota {
						return
					}
				}

				call := calls[(workerID+callIndex)%len(calls)]
				callStart := time.Now()
				err := call(client)
				lat := time.Since(callStart)

				atomic.AddInt64(&totalRequests, 1)
				atomic.AddInt64(&totalLatencyNanoseconds, lat.Nanoseconds())
				if err != nil {
					atomic.AddInt64(&totalErrors, 1)
				}
				updateMaxInt64(&maxLatencyNanoseconds, lat.Nanoseconds())

				callIndex++
			}
		}()
	}

	wg.Wait()
	close(progressDone)
	elapsed := time.Since(start)
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}

	finalRequests := atomic.LoadInt64(&totalRequests)
	finalErrors := atomic.LoadInt64(&totalErrors)
	finalLatencyNs := atomic.LoadInt64(&totalLatencyNanoseconds)
	finalMaxLatencyNs := atomic.LoadInt64(&maxLatencyNanoseconds)

	avgLatency := time.Duration(0)
	if finalRequests > 0 {
		avgLatency = time.Duration(finalLatencyNs / finalRequests)
	}

	rps := float64(finalRequests) / elapsed.Seconds()
	errorRate := 0.0
	if finalRequests > 0 {
		errorRate = float64(finalErrors) / float64(finalRequests)
	}

	t.Logf("RPC spam finished: elapsed=%s total=%d errors=%d errorRate=%.4f rps=%.0f avg=%s max=%s goroutines=%d",
		elapsed, finalRequests, finalErrors, errorRate, rps, avgLatency, time.Duration(finalMaxLatencyNs), runtime.NumGoroutine())

	if finalRequests == 0 {
		t.Fatalf("no RPC requests completed")
	}

	if cfg.MaxErrors == 0 {
		if finalErrors > 0 {
			t.Fatalf("got %d RPC errors (max-errors=0)", finalErrors)
		}
	} else if int(finalErrors) > cfg.MaxErrors {
		t.Fatalf("got %d RPC errors (max-errors=%d)", finalErrors, cfg.MaxErrors)
	}

	if errorRate > cfg.MaxErrorRate {
		t.Fatalf("error rate %.6f exceeded max-error-rate %.6f", errorRate, cfg.MaxErrorRate)
	}

	if cfg.TotalCalls == 0 && elapsed < cfg.Duration/2 {
		t.Fatalf("test finished too quickly (elapsed=%s duration=%s)", elapsed, cfg.Duration)
	}
}

func updateMaxInt64(target *int64, value int64) {
	for {
		current := atomic.LoadInt64(target)
		if value <= current {
			return
		}
		if atomic.CompareAndSwapInt64(target, current, value) {
			return
		}
	}
}

func connectWithRetry(rpcAddress string, timeout time.Duration) (*rpcclient.RPCClient, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		client, err := rpcclient.NewRPCClient(rpcAddress)
		if err == nil {
			return client, nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return nil, lastErr
}

func parseMethods(methodsCSV string, selectedTipHash func() string) ([]rpcCall, error) {
	raw := strings.TrimSpace(methodsCSV)
	if raw == "" {
		return nil, nil
	}

	methodNames := strings.Split(raw, ",")
	calls := make([]rpcCall, 0, len(methodNames))

	registry := map[string]rpcCall{
		"GetInfo": func(client *rpcclient.RPCClient) error {
			_, err := client.GetInfo()
			return err
		},
		"GetBlockCount": func(client *rpcclient.RPCClient) error {
			_, err := client.GetBlockCount()
			return err
		},
		"GetBlockDAGInfo": func(client *rpcclient.RPCClient) error {
			_, err := client.GetBlockDAGInfo()
			return err
		},
		"GetCoinSupply": func(client *rpcclient.RPCClient) error {
			_, err := client.GetCoinSupply()
			return err
		},
		"GetVirtualSelectedParentBlueScore": func(client *rpcclient.RPCClient) error {
			_, err := client.GetVirtualSelectedParentBlueScore()
			return err
		},
		"GetSelectedTipHash": func(client *rpcclient.RPCClient) error {
			_, err := client.GetSelectedTipHash()
			return err
		},
		"GetBlock": func(client *rpcclient.RPCClient) error {
			hash := ""
			if selectedTipHash != nil {
				hash = selectedTipHash()
			}
			if hash == "" {
				// Fall back to querying on-demand if the cache is empty.
				resp, err := client.GetSelectedTipHash()
				if err != nil {
					return err
				}
				hash = resp.SelectedTipHash
			}
			_, err := client.GetBlock(hash, false)
			return err
		},
	}

	for _, name := range methodNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		call, ok := registry[name]
		if !ok {
			return nil, &unknownMethodError{name: name}
		}
		calls = append(calls, call)
	}
	return calls, nil
}

type unknownMethodError struct{ name string }

func (e *unknownMethodError) Error() string {
	return "unknown method: " + e.name
}
