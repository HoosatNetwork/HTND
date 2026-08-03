package pebble

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/bloom"
	"github.com/cockroachdb/pebble/v2/sstable"
)

func Options(cacheSizeMiB int) *pebble.Options {
	// ────────────────────────────────────────────────
	// Bloom filter configuration (16 bits ~0.06% FP)
	// ────────────────────────────────────────────────
	bloomBitsPerKey := 16
	if v := os.Getenv("HTND_BLOOM_FILTER_LEVEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 && n <= 20 {
			bloomBitsPerKey = n
		}
	}
	bloomPolicy := bloom.FilterPolicy(bloomBitsPerKey)

	// ────────────────────────────────────────────────
	// Memtable tuning
	// ────────────────────────────────────────────────
	const defaultMemTableMB = 64
	memTableBytes := int64(defaultMemTableMB) << 20
	if v := os.Getenv("HTND_MEMTABLE_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 16 {
			memTableBytes = int64(mb) << 20
		}
	}

	baseFileSize := memTableBytes
	if baseFileSize < 32<<20 {
		baseFileSize = 32 << 20
	} else if baseFileSize > 128<<20 {
		baseFileSize = 128 << 20
	}

	// ────────────────────────────────────────────────
	// Block cache
	// ────────────────────────────────────────────────
	cacheBytes := int64(4096) << 20 // 4 GiB default
	if cacheSizeMiB > 0 {
		cacheBytes = int64(cacheSizeMiB) << 20
	}

	// ────────────────────────────────────────────────
	// Core Pebble options
	// ────────────────────────────────────────────────
	opts := &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,

		Cache: pebble.NewCache(cacheBytes),

		MemTableSize:                uint64(memTableBytes),
		MemTableStopWritesThreshold: getEnvInt("HTND_MEMTABLE_STOP_THRESHOLD", 8),

		FlushSplitBytes: baseFileSize,

		// Healthy L0 pacing: start compacting at 4 files, stall writes at 32
		L0CompactionThreshold:     getEnvInt("HTND_L0_COMPACTION_THRESHOLD", 4),
		L0StopWritesThreshold:     getEnvInt("HTND_L0_STOP_WRITES_THRESHOLD", 32),
		L0CompactionFileThreshold: getEnvInt("HTND_L0_COMPACTION_FILE_THRESHOLD", 8),

		TargetFileSizes: [7]int64{
			baseFileSize,      // L0
			baseFileSize * 2,  // L1
			baseFileSize * 4,  // L2
			baseFileSize * 8,  // L3
			baseFileSize * 16, // L4
			baseFileSize * 32, // L5
			baseFileSize * 64, // L6
		},

		MaxManifestFileSize: 128 << 20,
		MaxOpenFiles:        getEnvInt("HTND_PEBBLE_MAX_OPEN_FILES", 32768),

		DisableWAL: envBool("HTND_PEBBLE_DISABLE_WAL") || envBool("HTND_TEST_MODE"),

		CompactionConcurrencyRange: func() (int, int) { return 2, 6 },

		Levels: [7]pebble.LevelOptions{
			{ // L0 — Enable Bloom Filters to stop point-lookup degradation!
				BlockSize:      16 << 10,
				IndexBlockSize: 128 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   nil,
			},
			{ // L1
				BlockSize:      16 << 10,
				IndexBlockSize: 128 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L2
				BlockSize:      16 << 10,
				IndexBlockSize: 128 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L3
				BlockSize:      16 << 10,
				IndexBlockSize: 128 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L4
				BlockSize:      16 << 10,
				IndexBlockSize: 128 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L5
				BlockSize:      16 << 10,
				IndexBlockSize: 128 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L6
				BlockSize:      16 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
		},
	}

	// ────────────────────────────────────────────────
	// Experimental & Performance Overrides
	// ────────────────────────────────────────────────
	opts.Experimental.L0CompactionConcurrency = getEnvInt("HTND_L0_COMPACTION_CONCURRENCY", 4)

	// Disable read-triggered compaction to stop NVMe IO spikes during sync
	opts.Experimental.ReadCompactionRate = 0

	// Disable Value Separation for point-lookup heavy workloads.
	// (Or set MinimumSize to >= 32768 if storing large multi-KB payloads)
	opts.Experimental.ValueSeparationPolicy = func() pebble.ValueSeparationPolicy {
		return pebble.ValueSeparationPolicy{
			Enabled: false, // FIXED: Set to false to eliminate double I/O lookups
		}
	}

	if envBool("HTND_PEBBLE_LOG_EVENTS") {
		minDurMs := getEnvInt("HTND_PEBBLE_LOG_EVENTS_MIN_MS", 250)
		minDuration := time.Duration(minDurMs) * time.Millisecond
		opts.Logger = pebbleLoggerAdapter{}
		opts.EventListener = newLoggingEventListener(minDuration)
	}

	opts.EnsureDefaults()
	return opts
}

// ──────────────────────────────────────────────────────────────
// Helpers (unchanged)
// ──────────────────────────────────────────────────────────────

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultVal
}

func envBool(key string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func newLoggingEventListener(minDuration time.Duration) *pebble.EventListener {
	return &pebble.EventListener{
		BackgroundError: func(err error) {
			log.Errorf("[pebble] background error: %v", err)
		},
		WriteStallBegin: func(info pebble.WriteStallBeginInfo) {
			log.Warnf("[pebble] write stall begin: %s", info.Reason)
		},
		WriteStallEnd: func() {
			log.Warnf("[pebble] write stall end")
		},
		CompactionEnd: func(info pebble.CompactionInfo) {
			if info.Err != nil || info.TotalDuration >= minDuration {
				log.Infof("[pebble] compaction end: duration=%s err=%v input=%v output=%v", info.TotalDuration, info.Err, info.Input, info.Output)
			}
		},
		FlushEnd: func(info pebble.FlushInfo) {
			if info.Err != nil || info.TotalDuration >= minDuration {
				log.Infof("[pebble] flush end: duration=%s err=%v", info.TotalDuration, info.Err)
			}
		},
		DiskSlow: func(info pebble.DiskSlowInfo) {
			log.Warnf("[pebble] disk slow: opType=%s path=%s write=%d dur=%s", info.OpType, info.Path, info.WriteSize, info.Duration)
		},
	}
}
