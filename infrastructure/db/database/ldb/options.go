package ldb

import (
	"os"
	"strconv"

	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// Options returns a leveldb opt.Options struct optimized for Kaspa's high block rate (33 blocks/s, 1000 tx/block).
func Options() opt.Options {
	bloomFilterLevel := int(16)
	if v := os.Getenv("HTND_BLOOM_FILTER_LEVEL"); v != "" {
		if levl, err := strconv.Atoi(v); err == nil && levl > 0 {
			bloomFilterLevel = int(levl)
		}
	} else if v := os.Getenv("BLOOM_FILTER_LEVEL"); v != "" {
		if levl, err := strconv.Atoi(v); err == nil && levl > 0 {
			bloomFilterLevel = int(levl)
		}
	}
	opts := opt.Options{
		Compression:            opt.SnappyCompression,                   // Balances speed and storage efficiency
		NoSync:                 false,                                   // Ensures data integrity for high-value blockchain data
		WriteBuffer:            64 * opt.MiB,                            // Larger buffer to handle bursty writes
		BlockCacheCapacity:     1024 * opt.MiB,                          // Larger cache for frequent reads
		Filter:                 filter.NewBloomFilter(bloomFilterLevel), // Bloom filter for efficient key lookups
		OpenFilesCacheCapacity: 1024,                                    // Higher file handle cache for concurrent access
		CompactionTableSize:    32 * opt.MiB,                            // Larger SST files to reduce compaction frequency
		CompactionTotalSize:    1024 * opt.MiB,                          // Larger total size before compaction to reduce I/O
		// Reduce likelihood of long write pauses during heavy sequential ingestion
		// by letting LevelDB accumulate more L0 tables before applying hard backpressure.
		// Default WriteL0PauseTrigger is ~12; raising it helps avoid stalls at the cost of
		// temporary higher L0 count (still safe for one-shot tools like ldbtool).
		CompactionL0Trigger:    8,  // start compaction a bit later to form bigger tables
		WriteL0SlowdownTrigger: 24, // start slowing down later
		WriteL0PauseTrigger:    48, // hard pause threshold raised to reduce long pauses
	}

	// Allow runtime overrides via environment for tooling scenarios.
	// These are best-effort; invalid values are ignored.
	if v := os.Getenv("KSDB_COMPACTION_L0_TRIGGER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.CompactionL0Trigger = n
		}
	}
	if v := os.Getenv("KSDB_WRITE_L0_SLOWDOWN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.WriteL0SlowdownTrigger = n
		}
	}
	if v := os.Getenv("KSDB_WRITE_L0_PAUSE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.WriteL0PauseTrigger = n
		}
	}

	return opts
}
