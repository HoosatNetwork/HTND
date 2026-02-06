package rpcstats

import (
	"net"
	"sort"
	"sync"
	"time"

	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var log = logger.RegisterSubSystem("RPCSTATS")
var spawn = panics.GoroutineWrapperFunc(log)

const (
	// statsLoggingInterval is how often we log the RPC statistics
	statsLoggingInterval = 1 * time.Minute
)

// Stats tracks RPC request statistics
type Stats struct {
	sync.RWMutex
	requestsByIP     map[string]uint64
	requestsByMethod map[string]uint64
	totalRequests    uint64
	startTime        time.Time
	stopChan         chan struct{}
	stopped          bool
}

// IPRequestCount represents a request count for a specific IP
type IPRequestCount struct {
	IP    string
	Count uint64
}

// NewStats creates a new RPC statistics tracker
func NewStats() *Stats {
	return &Stats{
		requestsByIP:     make(map[string]uint64),
		requestsByMethod: make(map[string]uint64),
		totalRequests:    0,
		startTime:        time.Now(),
		stopChan:         make(chan struct{}),
	}
}

// Start begins the periodic logging of RPC statistics
func (s *Stats) Start() {
	spawn("rpcstats-logging", func() {
		ticker := time.NewTicker(statsLoggingInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.logAndReset()
			case <-s.stopChan:
				return
			}
		}
	})
	log.Infof("RPC statistics tracking started (logging every %v)", statsLoggingInterval)
}

// Stop stops the statistics tracker
func (s *Stats) Stop() {
	s.Lock()
	defer s.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	close(s.stopChan)
	log.Infof("RPC statistics tracking stopped")
}

// RecordRequest records an RPC request from a given IP address
func (s *Stats) RecordRequest(address string, method string) {
	// Extract IP from address (which may include port)
	ip := extractIP(address)

	s.Lock()
	defer s.Unlock()

	s.requestsByIP[ip]++
	s.requestsByMethod[method]++
	s.totalRequests++
}

// extractIP extracts just the IP address from an address that may include a port
func extractIP(address string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		// If there's no port, return the address as-is
		return address
	}
	return host
}

// logAndReset logs the current statistics and resets the counters
func (s *Stats) logAndReset() {
	s.Lock()
	defer s.Unlock()

	elapsed := time.Since(s.startTime)

	if s.totalRequests == 0 {
		log.Debugf("RPC Stats: No requests in the last %v", elapsed.Round(time.Second))
		s.startTime = time.Now()
		return
	}

	// Calculate requests per minute
	minutes := elapsed.Minutes()
	if minutes < 0.01 {
		minutes = 0.01 // Avoid division by zero
	}
	requestsPerMinute := float64(s.totalRequests) / minutes

	log.Infof("RPC Stats: %d total requests in %v (%.1f req/min)",
		s.totalRequests, elapsed.Round(time.Second), requestsPerMinute)

	// Log top IPs by request count
	topIPs := s.getTopIPs(10)
	if len(topIPs) > 0 {
		log.Infof("RPC Stats: Top requesting IPs:")
		for i, ipCount := range topIPs {
			percentage := float64(ipCount.Count) / float64(s.totalRequests) * 100
			log.Infof("  %d. %s: %d requests (%.1f%%)", i+1, ipCount.IP, ipCount.Count, percentage)
		}
	}

	// Log request counts by method
	if len(s.requestsByMethod) > 0 {
		log.Debugf("RPC Stats: Requests by method:")
		methodCounts := make([]struct {
			Method string
			Count  uint64
		}, 0, len(s.requestsByMethod))
		for method, count := range s.requestsByMethod {
			methodCounts = append(methodCounts, struct {
				Method string
				Count  uint64
			}{method, count})
		}
		sort.Slice(methodCounts, func(i, j int) bool {
			return methodCounts[i].Count > methodCounts[j].Count
		})
		for _, mc := range methodCounts {
			log.Debugf("  %s: %d", mc.Method, mc.Count)
		}
	}

	// Reset counters
	s.requestsByIP = make(map[string]uint64)
	s.requestsByMethod = make(map[string]uint64)
	s.totalRequests = 0
	s.startTime = time.Now()
}

// getTopIPs returns the top N IPs by request count
func (s *Stats) getTopIPs(n int) []IPRequestCount {
	ipCounts := make([]IPRequestCount, 0, len(s.requestsByIP))
	for ip, count := range s.requestsByIP {
		ipCounts = append(ipCounts, IPRequestCount{IP: ip, Count: count})
	}

	// Sort by count descending
	sort.Slice(ipCounts, func(i, j int) bool {
		return ipCounts[i].Count > ipCounts[j].Count
	})

	if len(ipCounts) > n {
		ipCounts = ipCounts[:n]
	}

	return ipCounts
}

// GetCurrentStats returns the current statistics without resetting
func (s *Stats) GetCurrentStats() (totalRequests uint64, requestsByIP map[string]uint64, requestsByMethod map[string]uint64) {
	s.RLock()
	defer s.RUnlock()

	// Make copies of the maps
	ipCopy := make(map[string]uint64, len(s.requestsByIP))
	for k, v := range s.requestsByIP {
		ipCopy[k] = v
	}

	methodCopy := make(map[string]uint64, len(s.requestsByMethod))
	for k, v := range s.requestsByMethod {
		methodCopy[k] = v
	}

	return s.totalRequests, ipCopy, methodCopy
}
