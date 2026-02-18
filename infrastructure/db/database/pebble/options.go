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

// Options returns a Pebble configuration heavily tuned for blockchain node workload:
//   - Very high rate of point lookups (UTXO spends, script/hash lookups, headers, etc.)
//   - Sustained high write throughput during IBD, catch-up sync and parallel block processing
//   - Assumes NVMe SSD + 16–64 GiB RAM class hardware in 2026
//
// Key modern features (as of 2026 Pebble):
// - FormatMajorVersion: pebble.FormatNewest → enables columnar blocks (colblk) by default for faster point/range reads
// - Value separation → enabled by default (values ≥256 bytes stored in separate blob files) → major write-amp reduction
// - No need to set ValueSeparationPolicy unless customizing threshold
func Options(cacheSizeMiB int) *pebble.Options {
	// ────────────────────────────────────────────────
	// Bloom filter configuration
	// 15 bits/key → good balance: low false positives (~0.06%) for point lookups
	// ────────────────────────────────────────────────
	bloomBitsPerKey := 15
	if v := os.Getenv("HTND_BLOOM_FILTER_LEVEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 && n <= 20 {
			bloomBitsPerKey = n
		}
	}
	bloomPolicy := bloom.FilterPolicy(bloomBitsPerKey)

	// ────────────────────────────────────────────────
	// Memtable tuning
	// ────────────────────────────────────────────────
	const (
		defaultMemTableMB           = 128
		defaultMemTablesBeforeStall = 6
	)

	memTableBytes := int64(defaultMemTableMB) << 20
	if v := os.Getenv("HTND_MEMTABLE_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 16 {
			memTableBytes = int64(mb) << 20
		}
	}

	memTableStopThreshold := defaultMemTablesBeforeStall
	if v := os.Getenv("HTND_MEMTABLE_STOP_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 2 {
			memTableStopThreshold = n
		}
	}

	// ────────────────────────────────────────────────
	// Target SST file size at base level
	// ────────────────────────────────────────────────
	baseFileSize := memTableBytes / 2
	const (
		minBaseFileSize = 32 << 20
		maxBaseFileSize = 128 << 20
	)
	if baseFileSize < minBaseFileSize {
		baseFileSize = minBaseFileSize
	}
	if baseFileSize > maxBaseFileSize {
		baseFileSize = maxBaseFileSize
	}
	if v := os.Getenv("HTND_BASE_FILE_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb >= 16 {
			baseFileSize = int64(mb) << 20
		}
	}

	// ────────────────────────────────────────────────
	// Block cache – aim higher in 2026 (8–16 GiB realistic)
	// ────────────────────────────────────────────────
	cacheBytes := int64(8192) << 20 // 8 GiB default – increase for better hit rate
	if cacheSizeMiB > 0 {
		cacheBytes = int64(cacheSizeMiB) << 20
	}
	if v := os.Getenv("HTND_PEBBLE_CACHE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 256 {
			cacheBytes = int64(mb) << 20
		}
	}

	// ────────────────────────────────────────────────
	// Core Pebble options
	// ────────────────────────────────────────────────
	opts := &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest, // Enables columnar blocks + value separation by default

		Cache: pebble.NewCache(cacheBytes),

		MemTableSize:                uint64(memTableBytes),
		MemTableStopWritesThreshold: memTableStopThreshold,

		FlushSplitBytes: baseFileSize,

		L0CompactionThreshold:     getEnvInt("HTND_L0_COMPACTION_THRESHOLD", 16),
		L0StopWritesThreshold:     getEnvInt("HTND_L0_STOP_WRITES_THRESHOLD", 48),
		L0CompactionFileThreshold: getEnvInt("HTND_L0_COMPACTION_FILE_THRESHOLD", 16),

		TargetFileSizes: [7]int64{
			baseFileSize,       // L0
			baseFileSize * 4,   // L1
			baseFileSize * 12,  // L2
			baseFileSize * 32,  // L3
			baseFileSize * 64,  // L4
			baseFileSize * 128, // L5
			baseFileSize * 256, // L6
		},

		MaxManifestFileSize: 512 << 20,
		MaxOpenFiles:        getEnvInt("HTND_PEBBLE_MAX_OPEN_FILES", 4096), // Bump for more SSTs

		DisableWAL:      false,
		WALBytesPerSync: 4 << 20,
		BytesPerSync:    4 << 20,

		CompactionConcurrencyRange: func() (int, int) { return 4, 8 },

		Levels: [7]pebble.LevelOptions{
			{ // L0
				BlockSize:      32 << 10,
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L1
				BlockSize:      32 << 10,
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L2
				BlockSize:      32 << 10,
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L3
				BlockSize:      32 << 10,
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L4
				BlockSize:      32 << 10,
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L5
				BlockSize:      32 << 10,
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L6
				BlockSize:      64 << 10,
				IndexBlockSize: 64 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
		},
	}

	// Optional: disable flush splitting
	if envBool("HTND_PEBBLE_DISABLE_FLUSH_SPLIT") {
		opts.FlushSplitBytes = 0
	}

	// ────────────────────────────────────────────────
	// Experimental / advanced controls
	// ────────────────────────────────────────────────

	// How many L0 compactions can run concurrently
	opts.Experimental.L0CompactionConcurrency = getEnvInt("HTND_L0_COMPACTION_CONCURRENCY", 6) // Increased from 4 to handle more L0 files

	// Trigger extra compaction workers when debt (pending bytes) is high
	opts.Experimental.CompactionDebtConcurrency = uint64(getEnvInt("HTND_COMPACTION_DEBT_CONCURRENCY_GB", 8)) << 30

	// Read-triggered compactions: compact hot-read data more aggressively
	// Helpful during long IBD phases with repeated ancestor / window lookups
	opts.Experimental.ReadCompactionRate = 32 << 20 // 32 MiB/s – moderate aggressiveness
	opts.Experimental.ReadSamplingMultiplier = 8    // sample 1/8 reads for triggering

	if v := os.Getenv("HTND_READ_COMPACTION_RATE_KB"); v != "" {
		if kb, err := strconv.Atoi(v); err == nil && kb > 0 {
			opts.Experimental.ReadCompactionRate = int64(kb) << 10
		}
	}
	if v := os.Getenv("HTND_READ_SAMPLING_MULTIPLIER"); v != "" {
		if m, err := strconv.Atoi(v); err == nil && m >= 1 {
			opts.Experimental.ReadSamplingMultiplier = int64(m)
		}
	}

	opts.Experimental.ValueSeparationPolicy = func() pebble.ValueSeparationPolicy {
		return pebble.ValueSeparationPolicy{Enabled: true, MinimumSize: 128}
	}

	// ────────────────────────────────────────────────
	// Optional detailed event logging (useful for tuning & debugging)
	// Logging (optional)
	// ────────────────────────────────────────────────
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
			// log.Errorf("[pebble] background error: %v", err)
		},
		WriteStallBegin: func(info pebble.WriteStallBeginInfo) {
			// log.Warnf("[pebble] write stall begin: %s", info.Reason)
		},
		WriteStallEnd: func() {
			// log.Warnf("[pebble] write stall end")
		},
		CompactionEnd: func(info pebble.CompactionInfo) {
			// if info.Err != nil || info.TotalDuration >= minDuration { ... }
		},
		FlushEnd: func(info pebble.FlushInfo) {
			// if info.Err != nil || info.TotalDuration >= minDuration { ... }
		},
		DiskSlow: func(info pebble.DiskSlowInfo) {
			// log.Warnf("[pebble] disk slow: op=%s path=%s write=%d dur=%s", ...)
		},
	}
}
