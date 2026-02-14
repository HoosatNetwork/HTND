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
// Most important tuning axes for this workload:
//  1. Bloom filter strength → point lookup hit rate
//  2. Memtable size & stall thresholds → write burst tolerance
//  3. L0 file count tolerance → write throughput vs read amplification
//  4. SST sizes & block sizes → cache efficiency & sequential read performance
//  5. Block cache size → overall lookup hit rate during validation/IBD
//
// Performance optimizations applied:
//   - Reduced bloom filter bits from 14 to 12 for faster filter creation
//   - Increased L0 compaction thresholds to reduce compaction frequency
//   - Increased block sizes from 16KB to 32KB for better cache efficiency
//   - Changed L6 compression from Zstd to Snappy for better performance
//   - Increased L0 compaction concurrency to handle more files
func Options(cacheSizeMiB int) *pebble.Options {
	// ────────────────────────────────────────────────
	// Bloom filter configuration
	// Controls false-positive rate for point lookups.
	// Higher bits = lower false positives = fewer unnecessary SST reads = faster Gets
	// ────────────────────────────────────────────────
	// For high-performance workloads, balance between filter creation time and lookup efficiency
	// Reduced from 14 to 12 bits to speed up bloom filter creation during compaction
	bloomBitsPerKey := 12 // Optimized: faster filter creation, still good lookup performance
	if v := os.Getenv("HTND_BLOOM_FILTER_LEVEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 && n <= 20 {
			bloomBitsPerKey = n
		}
	}
	bloomPolicy := bloom.FilterPolicy(bloomBitsPerKey)

	// ────────────────────────────────────────────────
	// Memtable tuning
	// Larger memtables → fewer flushes → less L0 pressure → better sustained write rate
	// Smaller memtables → data reaches disk faster → lower memory usage during bursts
	// ────────────────────────────────────────────────
	const (
		defaultMemTableMB           = 128 // Larger than RocksDB/Pebble defaults – helps high TPS/IBD bursts
		defaultMemTablesBeforeStall = 6   // How many unflushed memtables before write stall
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
	// Larger files → better block cache & index locality → fewer files overall
	// Smaller files → more granular compactions → potentially lower write amplification
	// ────────────────────────────────────────────────
	baseFileSize := memTableBytes / 2 // Slightly larger than memtable → natural flush grouping
	const (
		minBaseFileSize = 32 << 20  // 32 MiB  – too small → too many files
		maxBaseFileSize = 128 << 20 // 128 MiB – reasonable cap for cache efficiency
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
	// Block cache (holds decompressed data blocks + filters + indexes)
	// Extremely important for point lookup performance during validation & IBD
	// ────────────────────────────────────────────────
	cacheBytes := int64(2048) << 20 // 2 GiB default – generous but realistic for 16–32 GiB nodes
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
		FormatMajorVersion: pebble.FormatNewest, // Use newest on-disk format (better performance & features)

		Cache: pebble.NewCache(cacheBytes),

		// Memtable settings – see above
		MemTableSize:                uint64(memTableBytes),
		MemTableStopWritesThreshold: memTableStopThreshold,

		// Split large memtable flushes into multiple L0 files
		// → Enables better parallel compaction out of L0
		FlushSplitBytes: baseFileSize,

		// L0-specific controls – MOST IMPORTANT for write throughput vs read amplification
		//
		// L0CompactionThreshold:     start compacting when this many L0 files overlap a key range
		// L0StopWritesThreshold:     HARD stop accepting writes when L0 reaches this file count
		// L0CompactionFileThreshold: trigger compaction when a single file overlaps this many files below
		//
		// Higher values → tolerate larger L0 → much higher sustained write rate
		// Lower values  → keep L0 small → lower point-lookup amplification (fewer tables checked)
		L0CompactionThreshold:     getEnvInt("HTND_L0_COMPACTION_THRESHOLD", 16),      // Increased: start compaction later
		L0StopWritesThreshold:     getEnvInt("HTND_L0_STOP_WRITES_THRESHOLD", 48),     // Increased: allow more L0 files before write stall
		L0CompactionFileThreshold: getEnvInt("HTND_L0_COMPACTION_FILE_THRESHOLD", 16), // Increased: reduce compaction frequency

		// Target file sizes per level – controls fan-out and eventual file count
		// Steeper growth → fewer files at deeper levels → better cache efficiency
		TargetFileSizes: [7]int64{
			baseFileSize,       // L0
			baseFileSize * 4,   // L1
			baseFileSize * 12,  // L2
			baseFileSize * 32,  // L3
			baseFileSize * 64,  // L4
			baseFileSize * 128, // L5
			baseFileSize * 256, // L6 – avoid excessively large cold files
		},

		MaxManifestFileSize: 512 << 20,                                     // 512 MiB – large enough to avoid frequent manifest rewrites
		MaxOpenFiles:        getEnvInt("HTND_PEBBLE_MAX_OPEN_FILES", 2000), // allow many open SSTs

		// Write-ahead log & fsync behavior
		// Larger sync sizes → fewer fsync calls → better NVMe throughput
		DisableWAL:      false,
		WALBytesPerSync: 4 << 20, // 4 MiB
		BytesPerSync:    4 << 20, // 4 MiB – good balance for modern SSDs

		// Allow more parallel compaction workers during high load
		CompactionConcurrencyRange: func() (int, int) { return 4, 8 },

		// Per-level block & index sizes + compression
		// Larger blocks → better sequential read throughput & compression ratio
		// Smaller blocks → better random access granularity
		Levels: [7]pebble.LevelOptions{
			{ // L0 – write-mostly, high churn
				BlockSize:      32 << 10, // Increased from 16 KiB for better performance
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L1
				BlockSize:      32 << 10, // Increased from 16 KiB
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L2
				BlockSize:      32 << 10, // Increased from 16 KiB
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L3
				BlockSize:      32 << 10, // Increased from 16 KiB
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L4
				BlockSize:      32 << 10, // Increased from 16 KiB
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L5
				BlockSize:      32 << 10, // Increased to 32 KiB for better sequential performance
				IndexBlockSize: 32 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L6 – mostly cold data, use faster compression than Zstd
				BlockSize:      64 << 10, // Larger blocks for cold data
				IndexBlockSize: 64 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression }, // Changed from Zstd to Snappy for performance
				FilterPolicy:   bloomPolicy,
			},
		},
	}

	// Optional: disable flush splitting (useful on very slow disks or specific tests)
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

	// ────────────────────────────────────────────────
	// Optional detailed event logging (useful for tuning & debugging)
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
// Helper functions (unchanged)
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

// newLoggingEventListener returns an event listener that logs significant operations
// Replace log calls with your actual logger (assumed to exist)
func newLoggingEventListener(minDuration time.Duration) *pebble.EventListener {
	return &pebble.EventListener{
		// Implement desired logging callbacks here.
		// Example placeholders only – customize as needed.
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
			// log.Warnf("[pebble] disk slow: op=%s path=%s write=%d dur=%s",
			// 	info.OpType, info.Path, info.WriteSize, info.Duration)
		},
	}
}
