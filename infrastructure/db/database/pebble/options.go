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
	// Ribbon/Bloom Filter Policy (10 bits ≈ 1% FP rate)
	// ────────────────────────────────────────────────
	bloomBitsPerKey := 10
	if v := os.Getenv("HTND_BLOOM_FILTER_LEVEL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 && n <= 20 {
			bloomBitsPerKey = n
		}
	}
	filterPolicy := bloom.FilterPolicy(bloomBitsPerKey)

	// ────────────────────────────────────────────────
	// Memtable Size & File Base Sizing
	// ────────────────────────────────────────────────
	const defaultMemTableMB = 128
	memTableBytes := int64(defaultMemTableMB) << 20
	if v := os.Getenv("HTND_MEMTABLE_SIZE_MB"); v != "" {
		if mb, err := strconv.Atoi(v); err == nil && mb >= 32 {
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
	// Block Cache
	// ────────────────────────────────────────────────
	cacheBytes := int64(4096) << 20 // 4 GiB default
	if cacheSizeMiB > 0 {
		cacheBytes = int64(cacheSizeMiB) << 20
	}

	opts := &pebble.Options{
		FormatMajorVersion: pebble.FormatNewest,

		Cache: pebble.NewCache(cacheBytes),

		MemTableSize:                uint64(memTableBytes),
		MemTableStopWritesThreshold: getEnvInt("HTND_MEMTABLE_STOP_THRESHOLD", 12),

		// ────────────────────────────────────────────────
		// Smooth Out OS Disk I/O & Prevent Stall Spikes
		// ────────────────────────────────────────────────
		BytesPerSync:    2 << 20, // 2 MiB periodic sync for SSTables prevents page cache dumps
		WALBytesPerSync: 1 << 20, // 1 MiB periodic sync for WAL writes

		FlushSplitBytes: baseFileSize,

		// Increased from 2 to 4: '2' causes extreme write amplification (WA) thrashing in L0
		L0CompactionThreshold:     getEnvInt("HTND_L0_COMPACTION_THRESHOLD", 4),
		L0StopWritesThreshold:     getEnvInt("HTND_L0_STOP_WRITES_THRESHOLD", 64),
		L0CompactionFileThreshold: getEnvInt("HTND_L0_COMPACTION_FILE_THRESHOLD", 16),

		LBaseMaxBytes: 64 << 20, // Sets explicit target size for L1

		TargetFileSizes: [7]int64{
			baseFileSize,
			baseFileSize * 2,
			baseFileSize * 4,
			baseFileSize * 8,
			baseFileSize * 16,
			baseFileSize * 32,
			baseFileSize * 64,
		},

		MaxManifestFileSize: 128 << 20,
		MaxOpenFiles:        getEnvInt("HTND_PEBBLE_MAX_OPEN_FILES", 32768),

		DisableWAL: !envBool("HTND_PEBBLE_DISABLE_WAL"),

		CompactionConcurrencyRange: func() (int, int) { return 4, 12 },

		// ────────────────────────────────────────────────
		// Level Configurations: Optimized Block Sizes & Compression
		// ────────────────────────────────────────────────
		Levels: [7]pebble.LevelOptions{
			{ // L0 — Fast uncompressed ingestion
				BlockSize:      16 << 10, // 16 KiB: better for point reads than 32 KiB
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   filterPolicy,
			},
			{ // L1
				BlockSize:      16 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.NoCompression },
				FilterPolicy:   filterPolicy,
			},
			{ // L2
				BlockSize:      16 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   filterPolicy,
			},
			{ // L3
				BlockSize:      16 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   filterPolicy,
			},
			{ // L4
				BlockSize:      16 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.SnappyCompression },
				FilterPolicy:   filterPolicy,
			},
			{ // L5 — Zstd for cold data space saving and higher cache density
				BlockSize:      16 << 10,
				IndexBlockSize: 256 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.ZstdCompression },
				FilterPolicy:   filterPolicy,
			},
			{ // L6
				BlockSize:      16 << 10,
				IndexBlockSize: 512 << 10,
				Compression:    func() *sstable.CompressionProfile { return sstable.ZstdCompression },
				FilterPolicy:   filterPolicy,
			},
		},
	}

	// ────────────────────────────────────────────────
	// Experimental Settings Tuning
	// ────────────────────────────────────────────────
	opts.Experimental.L0CompactionConcurrency = getEnvInt("HTND_L0_COMPACTION_CONCURRENCY", 2)
	opts.Experimental.CompactionDebtConcurrency = 256 << 20

	opts.Experimental.ReadCompactionRate = 0
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

// Helpers
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
