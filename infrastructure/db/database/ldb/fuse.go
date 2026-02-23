package ldb

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"sync/atomic"

	"github.com/pkg/errors"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// ConflictStrategy defines how to handle key collisions when fusing databases.
type ConflictStrategy int

const (
	// Overwrite means keys from later sources overwrite existing values in dest.
	Overwrite ConflictStrategy = iota
	// KeepExisting means existing keys in dest are preserved; conflicting source keys are skipped.
	KeepExisting
)

// FuseOptions controls the behavior of FuseLevelDB.
type FuseOptions struct {
	// CacheSizeMiB sets the cache and write buffer sizing for opened DBs.
	CacheSizeMiB int
	// BatchSize controls how many KV pairs are written per batch.
	BatchSize int
	// MaxBatchBytes caps the total bytes buffered in a batch before flushing (approximate: key+value sizes).
	// Helps reduce peak memory usage regardless of BatchSize. If 0, defaults to 8 MiB.
	MaxBatchBytes int
	// Strategy controls how to resolve key collisions.
	Strategy ConflictStrategy
	// CompactAfter optionally compacts the destination DB after a successful fuse.
	CompactAfter bool
	// ProgressInterval controls how often to log progress (every N keys). If 0, defaults to 100_000.
	ProgressInterval int
	// PipelineDepth controls how many batches can be queued for asynchronous writes.
	// A value of 1 means no overlap (synchronous). Defaults to 2 to overlap read and write IO
	// while keeping memory bounded by roughly PipelineDepth * MaxBatchBytes.
	PipelineDepth int
	// Adaptive throttling to avoid hitting LevelDB hard write pause on L0 file explosion.
	// When the number of L0 files exceeds ThrottleL0Start, we sleep briefly between batch flushes.
	// If it exceeds ThrottleL0Max and CompactOnStall is true, we trigger a background compaction.
	ThrottleL0Start int // default 8
	ThrottleL0Max   int // default 32
	ThrottleSleepMS int // default 50
	CompactOnStall  bool
}

// setDefaults fills zero-value options with sensible defaults.
func (o *FuseOptions) setDefaults() {
	// If CacheSizeMiB is 0, we now respect Options() defaults in NewLevelDB.
	// Leave zero as-is to use the defaults from leveldb.Options().
	if o.BatchSize <= 0 {
		o.BatchSize = 10_000
	}
	if o.MaxBatchBytes <= 0 {
		o.MaxBatchBytes = 8 * opt.MiB
	}
	if o.ProgressInterval <= 0 {
		o.ProgressInterval = 100_000
	}
	if o.PipelineDepth <= 0 {
		o.PipelineDepth = 2
	}
	if o.ThrottleL0Start <= 0 {
		o.ThrottleL0Start = 8
	}
	if o.ThrottleL0Max <= 0 {
		o.ThrottleL0Max = 32
	}
	if o.ThrottleSleepMS <= 0 {
		o.ThrottleSleepMS = 50
	}
	// Leave CompactOnStall as provided by caller (no implicit default here).
}

// FuseLevelDB merges one or more source LevelDB databases into a destination LevelDB database.
//
// Contract:
//   - Inputs: destPath (string), sourcePaths ([]string), options (FuseOptions).
//   - Behavior: iterates over all keys from each source and writes them to the destination using batched writes.
//     If the same key appears multiple times, resolution follows options.Strategy. Order of sourcePaths matters
//     for Overwrite strategy: later sources take precedence.
//   - Errors: returns on first unrecoverable error; iterator errors are propagated.
//   - Success: all keys from sources are present in dest following the resolution strategy. Optionally compacts.
func FuseLevelDB(destPath string, sourcePaths []string, opts FuseOptions) error {
	opts.setDefaults()

	if len(sourcePaths) == 0 {
		return errors.New("no source paths provided")
	}

	absDest, _ := filepath.Abs(destPath)

	// Open destination DB (create if missing)
	dest, err := NewLevelDB(destPath, opts.CacheSizeMiB)
	if err != nil {
		return errors.Wrap(err, "open destination leveldb")
	}
	defer func() { _ = dest.Close() }()

	// Preflight: if destination already has an extremely high number of L0 files
	// (e.g., from a previous aborted run), optionally compact it before starting to write.
	// This is controlled by opts.CompactOnStall so callers can disable compaction entirely
	// and choose a clean destination directory instead.
	if opts.CompactOnStall {
		if s, _ := dest.ldb.GetProperty("leveldb.num-files-at-level0"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n >= opts.ThrottleL0Max {
				log.Warnf("Destination has %d Level-0 files; running a preflight compaction before fuse...", n)
				compStart := time.Now()
				if err := dest.Compact(); err != nil {
					return errors.Wrap(err, "preflight compact destination")
				}
				compElapsed := time.Since(compStart)
				s2, _ := dest.ldb.GetProperty("leveldb.num-files-at-level0")
				log.Infof("Preflight compaction finished in %s; destination L0 now %s", compElapsed.Truncate(time.Second), s2)
			}
		}
	}

	var totalWritten int64 // accessed from heartbeat goroutine
	var totalSkipped int64
	started := time.Now()

	for i, srcPath := range sourcePaths {
		absSrc, _ := filepath.Abs(srcPath)
		if absSrc == absDest {
			return errors.Errorf("source path #%d equals destination: %s", i, srcPath)
		}

		log.Infof("Fusing source %d/%d from '%s' into '%s' (strategy=%v, batch=%d)", i+1, len(sourcePaths), srcPath, destPath, opts.Strategy, opts.BatchSize)

		src, err := NewLevelDB(srcPath, opts.CacheSizeMiB)
		if err != nil {
			return errors.Wrapf(err, "open source leveldb %s", srcPath)
		}

		// Use read options that avoid polluting the block cache during the sequential copy.
		// This significantly improves throughput for large scans and prevents evicting useful data.
		roNoCache := &opt.ReadOptions{DontFillCache: true}
		rwNoCache := &opt.WriteOptions{Sync: false}
		iter := src.ldb.NewIterator(nil, roNoCache)

		// Heartbeat progress logger (logs even if Put blocks)
		hbStop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					tw := atomic.LoadInt64(&totalWritten)
					ts := atomic.LoadInt64(&totalSkipped)
					elapsed := time.Since(started)
					rate := 0.0
					if elapsed > 0 {
						rate = float64(tw) / elapsed.Seconds()
					}
					// Also report current L0 file count to diagnose write stalls.
					l0s, _ := dest.ldb.GetProperty("leveldb.num-files-at-level0")
					log.Infof("Progress: %d keys written, %d keys skipped so far (%.1f keys/s) [L0=%s]", tw, ts, rate, l0s)
				case <-hbStop:
					return
				}
			}
		}()

		// Adaptive throttling helpers to reduce write stalls.
		writtenThisSource := 0
		getL0 := func() int {
			// leveldb.num-files-at-level0 returns a decimal string, or empty if unsupported
			s, _ := dest.ldb.GetProperty("leveldb.num-files-at-level0")
			if n, err := strconv.Atoi(s); err == nil {
				return n
			}
			return -1
		}
		lastManualCompact := time.Time{}
		maybeThrottle := func() {
			l0 := getL0()
			if l0 >= opts.ThrottleL0Start {
				// Sleep grows slightly with L0 to give compaction breathing room.
				extra := 0
				if l0 > opts.ThrottleL0Start {
					extra = (l0 - opts.ThrottleL0Start) * 5 // 5ms per file above start
				}
				sleep := time.Duration(opts.ThrottleSleepMS+extra) * time.Millisecond
				time.Sleep(sleep)
			}
			if opts.CompactOnStall && l0 >= opts.ThrottleL0Max {
				// Trigger an occasional full compaction to break prolonged stalls.
				if time.Since(lastManualCompact) > 15*time.Second {
					lastManualCompact = time.Now()
					go func() {
						if err := dest.Compact(); err != nil {
							log.Warnf("manual compact failed: %v", err)
						} else {
							log.Infof("manual compaction triggered to reduce L0 files (count=%d)", l0)
						}
					}()
				}
			}
		}

		// Check every N writes to avoid excessive property polls
		checkEvery := opts.BatchSize
		if checkEvery <= 0 {
			checkEvery = 10_000
		}

		for ok := iter.First(); ok; ok = iter.Next() {
			// IMPORTANT: Iterator Key/Value buffers are reused by goleveldb and only
			// valid until the next iterator movement. Copy them before writing to dest.
			// k := append([]byte(nil), iter.Key()...)
			// v := append([]byte(nil), iter.Value()...)
			k := bytes.Clone(iter.Key())
			v := bytes.Clone(iter.Value())

			if opts.Strategy == KeepExisting {
				exists, err := dest.ldb.Has(k, roNoCache)
				if err != nil {
					iter.Release()
					_ = src.Close()
					return errors.WithStack(err)
				}
				if exists {
					// Skip overwriting existing keys and count them separately.
					atomic.AddInt64(&totalSkipped, 1)
					continue
				}
			}

			// Periodically throttle based on L0 to avoid stalls (only if compaction assistance is enabled)
			if opts.CompactOnStall && (writtenThisSource%checkEvery) == 0 && writtenThisSource > 0 {
				maybeThrottle()
			}

			if err := dest.ldb.Put(k, v, rwNoCache); err != nil {
				iter.Release()
				_ = src.Close()
				return errors.WithStack(err)
			}

			writtenThisSource++
			atomic.AddInt64(&totalWritten, 1)
		}

		if err := iter.Error(); err != nil {
			close(hbStop)
			iter.Release()
			_ = src.Close()
			return errors.Wrapf(err, "iterator error while reading %s", srcPath)
		}
		iter.Release()

		// No batch flush needed in direct Put mode

		close(hbStop)
		if err := src.Close(); err != nil {
			return errors.Wrapf(err, "close source %s", srcPath)
		}

		log.Infof("Finished fusing source %d/%d ('%s'): %d keys written", i+1, len(sourcePaths), srcPath, writtenThisSource)
	}

	if opts.CompactAfter {
		log.Infof("Compacting destination database '%s'...", destPath)
		if err := dest.Compact(); err != nil {
			return errors.Wrap(err, "compact destination")
		}
	}

	elapsed := time.Since(started)
	rate := float64(0)
	if elapsed > 0 {
		rate = float64(totalWritten) / elapsed.Seconds()
	}
	log.Infof("Fuse complete: wrote a total of %d keys in %s (%.1f keys/s) into '%s'", totalWritten, elapsed.Truncate(time.Millisecond), rate, destPath)
	return nil
}

// String implements fmt.Stringer for ConflictStrategy for readable logs.
func (s ConflictStrategy) String() string { // optional, for logs only
	switch s {
	case Overwrite:
		return "Overwrite"
	case KeepExisting:
		return "KeepExisting"
	default:
		return fmt.Sprintf("ConflictStrategy(%d)", int(s))
	}
}

// CopyLevelDB copies all key-value pairs from srcPath into destPath using the provided options.
// It is a thin wrapper over FuseLevelDB with a single source.
// Note: This does not delete keys that already exist in dest but not in src.
// To strictly mirror src, remove the dest directory first or extend with a pruning step.
func CopyLevelDB(srcPath, destPath string, opts FuseOptions) error {
	// Strict logical copy using LevelDB API only (no file-level copying).
	return FuseLevelDB(destPath, []string{srcPath}, opts)
}
