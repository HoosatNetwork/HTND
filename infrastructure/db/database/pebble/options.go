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

// Options returns Pebble configuration tuned for HTND's workload:
// high block rate, frequent point lookups, sustained write throughput.
//
// Defaults are kept memory-safe (important especially on Windows).
func Options(cacheSizeMiB int) *pebble.Options {
	// ────────────────────────────────────────────────
	// Bloom filter (critical for point lookup performance)
	// ────────────────────────────────────────────────
	// Higher bits → fewer false positives → faster reads
	//   10 ≈ 1.0%, 12 ≈ 0.4%, 14 ≈ 0.1%, 16 ≈ 0.025%
	bloomBitsPerKey := 16 // default: aggressive for IBD / hash lookups

	if v := os.Getenv("HTND_BLOOM_FILTER_LEVEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			bloomBitsPerKey = n
		}
	}

	bloomPolicy := bloom.FilterPolicy(bloomBitsPerKey)

	// ────────────────────────────────────────────────
	// MemTable & write stall protection
	// ────────────────────────────────────────────────
	const (
		// Keep defaults conservative for memory-bounded nodes.
		// Effective memtable memory upper bound is:
		//   MemTableSize * MemTableStopWritesThreshold
		defaultMemTableMB           = 64
		defaultMemTablesBeforeStall = 8
	)

	memTableBytes := int64(defaultMemTableMB) << 20
	if v := os.Getenv("HTND_MEMTABLE_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			memTableBytes = int64(mb) << 20
		}
	}

	memTableStopThreshold := defaultMemTablesBeforeStall
	if v := os.Getenv("HTND_MEMTABLE_STOP_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			memTableStopThreshold = n
		}
	}

	// ────────────────────────────────────────────────
	// Target file sizes
	// ────────────────────────────────────────────────
	// For point lookups, extremely large sstables tend to hurt cache locality
	// (index/filter blocks) and make compactions heavier/longer.
	// We derive a sane default from MemTableSize but allow explicit override.
	//
	// Default: MemTableSize/4 clamped to [16MiB, 256MiB].
	baseFileSize := memTableBytes / 4
	const (
		minBaseFileSize = int64(16) << 20
		maxBaseFileSize = int64(256) << 20
	)
	if baseFileSize < minBaseFileSize {
		baseFileSize = minBaseFileSize
	}
	if baseFileSize > maxBaseFileSize {
		baseFileSize = maxBaseFileSize
	}
	if v := os.Getenv("HTND_BASE_FILE_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			baseFileSize = int64(mb) << 20
		}
	}

	// ────────────────────────────────────────────────
	// Block cache size
	// ────────────────────────────────────────────────
	// Default is 512MiB to stay within an 8GiB node budget.
	cacheBytes := int64(512) << 20
	if cacheSizeMiB > 0 {
		cacheBytes = int64(cacheSizeMiB) << 20
	}

	if v := os.Getenv("HTND_PEBBLE_CACHE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb > 0 {
			cacheBytes = int64(mb) << 20
		}
	}

	// ────────────────────────────────────────────────
	// Main Pebble options
	// ────────────────────────────────────────────────
	opts := &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest, // modern format, auto-migrates

		Cache: pebble.NewCache(cacheBytes),

		MemTableSize:                uint64(memTableBytes),
		MemTableStopWritesThreshold: memTableStopThreshold,

		// Split flushes into smaller L0 files for better compaction concurrency.
		// This helps reduce L0 read amplification during IBD and improves point lookups.
		FlushSplitBytes: baseFileSize,

		// L0 tuning – keep L0 read-amplification under control for faster point lookups.
		// Lower thresholds reduce point-lookup work at the cost of more compaction.
		L0CompactionThreshold:     getEnvInt("HTND_L0_COMPACTION_THRESHOLD", 6),
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

		MaxManifestFileSize: 128 << 20, // 128 MiB
		MaxOpenFiles:        16384,

		// WAL & sync behavior
		DisableWAL:      false,
		WALBytesPerSync: 512 << 10, // 512 KiB
		BytesPerSync:    1 << 20,   // 1 MiB

		// Dynamic compaction workers
		CompactionConcurrencyRange: func() (int, int) { return 2, 4 },

		// Per-level configuration
		Levels: [7]pebble.LevelOptions{
			{ // L0 – fastest ingestion
				BlockSize:      8 << 10,
				IndexBlockSize: 4 << 10,
				// Snappy improves read IO during IBD with minimal CPU overhead.
				Compression:  func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy: bloomPolicy,
			},
			{ // L1
				BlockSize:      8 << 10,
				IndexBlockSize: 4 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L2
				BlockSize:      8 << 10,
				IndexBlockSize: 4 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L3
				BlockSize:      8 << 10,
				IndexBlockSize: 4 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L4
				BlockSize:      8 << 10,
				IndexBlockSize: 4 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L5
				BlockSize:      8 << 10,
				IndexBlockSize: 4 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   bloomPolicy,
			},
			{ // L6 – cold data, better ratio
				BlockSize:      8 << 10,
				IndexBlockSize: 4 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.ZstdCompression },
				FilterPolicy:   bloomPolicy,
			},
		},
	}

	// Allow disabling flush splitting if desired (eg. for certain disks).
	if envBool("HTND_PEBBLE_DISABLE_FLUSH_SPLIT") {
		opts.FlushSplitBytes = 0
	}

	// Enable extra compaction concurrency as L0 read-amp/debt grows.
	// This helps avoid a large number of overlapping files, which makes point
	// lookups slower (more table probes, more iterator work).
	opts.Experimental.L0CompactionConcurrency = getEnvInt("HTND_L0_COMPACTION_CONCURRENCY", 2)
	opts.Experimental.CompactionDebtConcurrency = uint64(getEnvInt("HTND_COMPACTION_DEBT_CONCURRENCY_GB", 2)) << 30

	// Optional read-triggered compactions (can help point lookups if the DB is read-heavy).
	// Defaults are conservative/off because IBD tends to be write-heavy.
	if v := os.Getenv("HTND_READ_COMPACTION_RATE_KB"); v != "" {
		if kb, err := strconv.Atoi(v); err == nil && kb > 0 {
			opts.Experimental.ReadCompactionRate = int64(kb) << 10
		}
	}
	if v := os.Getenv("HTND_READ_SAMPLING_MULTIPLIER"); v != "" {
		if m, err := strconv.Atoi(v); err == nil {
			opts.Experimental.ReadSamplingMultiplier = int64(m)
		}
	}

	// ────────────────────────────────────────────────
	// Optional verbose event logging
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
// Helpers
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
			if info.Err != nil {
				log.Errorf("[pebble] compaction failed  job=%d  reason=%s  dur=%s  err=%v",
					info.JobID, info.Reason, info.TotalDuration, info.Err)
				return
			}
			if info.TotalDuration >= minDuration {
				log.Infof("[pebble] compaction  job=%d  reason=%s  dur=%s",
					info.JobID, info.Reason, info.TotalDuration)
			}
		},
		FlushEnd: func(info pebble.FlushInfo) {
			if info.Err != nil {
				log.Errorf("[pebble] flush failed  job=%d  reason=%s  dur=%s  err=%v",
					info.JobID, info.Reason, info.TotalDuration, info.Err)
				return
			}
			if info.TotalDuration >= minDuration {
				log.Infof("[pebble] flush  job=%d  reason=%s  input=%d  bytes=%d  ingest=%t  dur=%s",
					info.JobID, info.Reason, info.Input, info.InputBytes, info.Ingest, info.TotalDuration)
			}
		},
		DiskSlow: func(info pebble.DiskSlowInfo) {
			log.Warnf("[pebble] disk slow  op=%s  path=%s  write=%d  dur=%s",
				info.OpType, info.Path, info.WriteSize, info.Duration)
		},
	}
}
