package pebble

import (
	"os"
	"strconv"
	"sync"
)

const defaultMaxOpenFiles = 40860

// The default connection budgets in infrastructure/config are 500 inbound peers,
// 8 outbound peers and 500 RPC clients. Leave another 272 descriptors for listeners,
// WAL/manifest files, compaction output, logs and temporary files. This is headroom,
// not a process-wide cap: custom peer settings and multiple DBs can require more.
const fileDescriptorReserve = 1280

// Pebble's file cache has a minimum of 64 readers and additionally budgets 10
// non-cache files. Keep a small usable allowance when the soft limit is low.
const minMaxOpenFiles = 128

var logFileLimitOnce sync.Once

func maxOpenFiles(fileLimit func() (uint64, error)) int {
	requested := defaultMaxOpenFiles
	if value, set := os.LookupEnv("HTND_PEBBLE_MAX_OPEN_FILES"); set {
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			// Do not echo arbitrary environment contents into the log.
			log.Warnf("Invalid HTND_PEBBLE_MAX_OPEN_FILES; using default %d", defaultMaxOpenFiles)
		} else {
			requested = n
		}
	}

	limit, err := fileLimit()
	if err != nil {
		logFileLimitOnce.Do(func() {
			log.Warnf("Cannot read the open-file soft limit; Pebble MaxOpenFiles=%d is not clamped", requested)
		})
		return requested
	}

	// At low limits, reduce the reserve instead of preventing the datadir from
	// opening. Avoid unsigned underflow and never return zero (Pebble defaults it).
	reserve := uint64(fileDescriptorReserve)
	if limit < fileDescriptorReserve+minMaxOpenFiles {
		reserve = limit - min(limit, minMaxOpenFiles)
	}
	budget := max(uint64(1), limit-reserve)
	effective := requested
	if uint64(effective) > budget {
		effective = int(budget) // budget < requested, so the conversion cannot overflow
	}
	logFileLimitOnce.Do(func() {
		if reserve < fileDescriptorReserve {
			log.Warnf("Open-file soft limit is too low for the full Pebble descriptor reserve")
		}
		log.Infof("Pebble MaxOpenFiles=%d (requested=%d, soft limit=%d, reserve=%d)",
			effective, requested, limit, reserve)
	})
	return effective
}
