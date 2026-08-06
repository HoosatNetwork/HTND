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
	// Bloom filter (16 bits ≈ 0.06% FP) — mandatory for point lookups
	// ────────────────────────────────────────────────
	bloomBitsPerKey := 16
	if v := os.Getenv("HTND_BLOOM_FILTER_LEVEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 && n <= 20 {
			bloomBitsPerKey = n
		}
	}
	bloomPolicy := bloom.FilterPolicy(bloomBitsPerKey)

	// ────────────────────────────────────────────────
	// Memtable — larger = fewer flushes under 5k TPS
	// ────────────────────────────────────────────────
	const defaultMemTableMB = 128 // was 64
	memTableBytes := int64(defaultMemTableMB) << 20
	if v := os.Getenv("HTND_MEMTABLE_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb >= 32 {
			memTableBytes = int64(mb) << 20
		}
	}

	// Target L0 file size. Keep reasonably large to reduce file-count overhead
	// but not so large that a single flush/compaction becomes a latency spike.
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
	// Core options tuned for sustained high write rate + fast point reads
	// ────────────────────────────────────────────────
	opts := &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,

		Cache: pebble.NewCache(cacheBytes),

		MemTableSize:                uint64(memTableBytes),
		MemTableStopWritesThreshold: getEnvInt("HTND_MEMTABLE_STOP_THRESHOLD", 12), // was 8

		FlushSplitBytes: baseFileSize,

		// Aggressive L0 management: start compacting early, stall very late
		L0CompactionThreshold:     getEnvInt("HTND_L0_COMPACTION_THRESHOLD", 2),       // was 4
		L0StopWritesThreshold:     getEnvInt("HTND_L0_STOP_WRITES_THRESHOLD", 64),     // was 32
		L0CompactionFileThreshold: getEnvInt("HTND_L0_COMPACTION_FILE_THRESHOLD", 16), // was 8

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

		// WAL is required for production durability in a crypto node.
		// Only disable via env for pure benchmarks / test mode.
		DisableWAL: envBool("HTND_PEBBLE_DISABLE_WAL") || envBool("HTND_TEST_MODE"),

		// High concurrency range so NVMe + many cores can keep up with 5k TPS
		CompactionConcurrencyRange: func() (int, int) { return 4, 12 },

		Levels: [7]pebble.LevelOptions{
			{ // L0 — Bloom is mandatory (recent data is hot)
				BlockSize:      32 << 10, // 32 KiB – better sequential throughput on NVMe
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy, // FIXED: was nil
			},
			{ // L1
				BlockSize:      32 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L2
				BlockSize:      32 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L3
				BlockSize:      32 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L4
				BlockSize:      32 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L5
				BlockSize:      32 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L6
				BlockSize:      32 << 10,
				IndexBlockSize: 512 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
		},
	}

	// ────────────────────────────────────────────────
	// Experimental — aggressive compaction scaling
	// ────────────────────────────────────────────────
	// Lower values = concurrency increases sooner when L0 or debt rises.
	opts.Experimental.L0CompactionConcurrency = getEnvInt("HTND_L0_COMPACTION_CONCURRENCY", 1) // was 4
	opts.Experimental.CompactionDebtConcurrency = 256 << 20                                    // 256 MiB

	// Disable read-triggered compaction (avoids random IO spikes during sync-heavy paths)
	opts.Experimental.ReadCompactionRate = 0

	// Value separation is a net loss for typical blockchain workloads
	// (mostly small keys/values: hashes, amounts, short scripts).
	// Only enable if you store multi-KB blobs.
	opts.Experimental.ValueSeparationPolicy = func() pebble.ValueSeparationPolicy {
		return pebble.ValueSeparationPolicy{
			Enabled: false,
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
				log.Infof("[pebble] compaction end: duration=%s err=%v input=%v output=%v",
					info.TotalDuration, info.Err, info.Input, info.Output)
			}
		},
		FlushEnd: func(info pebble.FlushInfo) {
			if info.Err != nil || info.TotalDuration >= minDuration {
				log.Infof("[pebble] flush end: duration=%s err=%v", info.TotalDuration, info.Err)
			}
		},
		DiskSlow: func(info pebble.DiskSlowInfo) {
			log.Warnf("[pebble] disk slow: opType=%s path=%s write=%d dur=%s",
				info.OpType, info.Path, info.WriteSize, info.Duration)
		},
	}
}
