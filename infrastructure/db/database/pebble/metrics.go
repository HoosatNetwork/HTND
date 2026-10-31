package pebble

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/bloom"
)

const metricsLogInterval = 10 * time.Minute

// effectiveOptionsSummary deliberately lists only numeric and boolean settings,
// rather than Options.String(), which can contain paths and custom callbacks.
func effectiveOptionsSummary(opts *pebble.Options, format pebble.FormatMajorVersion) string {
	minCompactions, maxCompactions := opts.CompactionConcurrencyRange()
	bits := opts.Levels[0].FilterPolicy.(bloom.FilterPolicy)
	return fmt.Sprintf("cache_bytes=%d memtable_bytes=%d memtable_stop=%d "+
		"l0_compaction_sublevels=%d l0_stop_sublevels=%d l0_compaction_files=%d "+
		"compaction_concurrency=%d-%d max_open_files=%d bloom_bits_per_key=%d "+
		"format=%d value_separation=%t wal_enabled=%t bytes_per_sync=%d wal_bytes_per_sync=%d "+
		"read_compaction_rate=%d read_sampling_multiplier=%d",
		opts.Cache.MaxSize(), opts.MemTableSize, opts.MemTableStopWritesThreshold,
		opts.L0CompactionThreshold, opts.L0StopWritesThreshold, opts.L0CompactionFileThreshold,
		minCompactions, maxCompactions, opts.MaxOpenFiles, bits,
		format, opts.Experimental.ValueSeparationPolicy().Enabled, !opts.DisableWAL,
		opts.BytesPerSync, opts.WALBytesPerSync,
		opts.Experimental.ReadCompactionRate, opts.Experimental.ReadSamplingMultiplier)
}

func metricsSummary(m *pebble.Metrics, begins, ends uint64) string {
	return fmt.Sprintf("l0_files=%d l0_sublevels=%d compaction_debt_bytes=%d "+
		"memtables=%d memtable_bytes=%d zombie_memtables=%d zombie_memtable_bytes=%d "+
		"write_stall_begins=%d write_stall_ends=%d",
		m.Levels[0].TablesCount, m.Levels[0].Sublevels, m.Compact.EstimatedDebt,
		m.MemTable.Count, m.MemTable.Size, m.MemTable.ZombieCount, m.MemTable.ZombieSize,
		begins, ends)
}

type metricsLogger struct {
	begins atomic.Uint64
	ends   atomic.Uint64
	wake   chan struct{}
	stop   chan struct{}
	done   chan struct{}
}

func newMetricsLogger(opts *pebble.Options) *metricsLogger {
	m := &metricsLogger{
		wake: make(chan struct{}, 1),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	// Pebble invokes these callbacks under its mutex. Never query the database
	// or wait for the logging worker here. Coalesce wakeups, not event counts.
	listener := pebble.EventListener{
		WriteStallBegin: func(pebble.WriteStallBeginInfo) {
			m.begins.Add(1)
			m.notify()
		},
		WriteStallEnd: func() {
			m.ends.Add(1)
			m.notify()
		},
	}
	combined := pebble.TeeEventListener(*opts.EventListener, listener)
	opts.EventListener = &combined
	return m
}

func (m *metricsLogger) notify() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func observedDB(db *pebble.DB, opts *pebble.Options, metrics *metricsLogger) *DB {
	log.Infof("[pebble] effective options: %s", effectiveOptionsSummary(opts, db.FormatMajorVersion()))
	go metrics.run(db.Metrics, metricsLogInterval, func(event, summary string) {
		log.Infof("[pebble] metrics (%s): %s", event, summary)
	})
	return &DB{db: db, metrics: metrics, walDisabled: opts.DisableWAL}
}

// Event-triggered samples are taken asynchronously, after Pebble releases its
// mutex. Several rapid transitions may share a sample; counters are cumulative
// since open. A sample is diagnostic, not an atomic snapshot of a stall boundary.
func (m *metricsLogger) run(metrics func() *pebble.Metrics, interval time.Duration, report func(string, string)) {
	defer close(m.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastBegins, lastEnds uint64
	defer func() {
		// Shutdown may win the select while a final stall notification is queued.
		// Account for it while the database is still open.
		ends, begins := m.ends.Load(), m.begins.Load()
		if begins != lastBegins || ends != lastEnds {
			report("write-stall-before-close", metricsSummary(metrics(), begins, ends))
		}
	}()
	for {
		select {
		case <-m.stop:
			return
		default:
		}
		event := "periodic"
		select {
		case <-m.stop:
			return
		case <-ticker.C:
		case <-m.wake:
			event = "write-stall"
		}
		// Load ends first so a concurrent begin/end pair cannot report more
		// ends than begins. Both counters have a single ordered Pebble producer.
		ends, begins := m.ends.Load(), m.begins.Load()
		if begins != lastBegins && ends != lastEnds {
			event = "write-stall-begin/end"
		} else if begins != lastBegins {
			event = "write-stall-begin"
		} else if ends != lastEnds {
			event = "write-stall-end"
		}
		report(event, metricsSummary(metrics(), begins, ends))
		lastBegins, lastEnds = begins, ends
	}
}

func (m *metricsLogger) close() {
	close(m.stop)
	<-m.done
}
