package pebble

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/cockroachdb/pebble/v2"
)

func clearOptionsEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"HTND_BLOOM_FILTER_LEVEL", "HTND_MEMTABLE_SIZE_MB", "HTND_MEMTABLE_STOP_THRESHOLD",
		"HTND_PEBBLE_CACHE_MB", "HTND_PEBBLE_MAX_OPEN_FILES", "HTND_L0_COMPACTION_THRESHOLD",
		"HTND_L0_STOP_WRITES_THRESHOLD", "HTND_L0_COMPACTION_FILE_THRESHOLD",
		"HTND_PEBBLE_DISABLE_WAL", "HTND_TEST_MODE", "HTND_PEBBLE_LOG_EVENTS",
	} {
		t.Setenv(name, "")
	}
}

// This integration test uses only the pre-existing open API, so it also runs
// against the parent revision and fails on missing output, not a missing symbol.
func TestOpenLogsEffectiveOptions(t *testing.T) {
	clearOptionsEnv(t)
	for _, open := range []struct {
		name string
		fn   func(string, int) (*DB, error)
	}{{"node", NewPebbleDB}, {"offline", OpenPebbleDB}} {
		t.Run(open.name, func(t *testing.T) {
			var output bytes.Buffer
			backend := logger.NewBackendWithFlags(0)
			if err := backend.AddLogWriter(nopLogCloser{&output}, logger.LevelInfo); err != nil {
				t.Fatal(err)
			}
			oldLog := log
			log = backend.Logger("HTNDB")
			log.SetLevel(logger.LevelInfo)
			if err := backend.Run(); err != nil {
				t.Fatal(err)
			}
			defer func() { log = oldLog }()
			db, err := open.fn(filepath.Join(t.TempDir(), "secret-datadir"), 8)
			if err != nil {
				backend.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				backend.Close()
				t.Fatal(err)
			}
			backend.Close()
			got := output.String()
			if strings.Count(got, "effective options:") != 1 {
				t.Fatalf("want exactly one options line, got %q", got)
			}
			for _, want := range []string{
				"cache_bytes=8388608", "memtable_bytes=67108864", "memtable_stop=8",
				"l0_compaction_sublevels=8", "l0_stop_sublevels=40", "l0_compaction_files=6",
				"compaction_concurrency=4-8", "max_open_files=40860", "bloom_bits_per_key=10",
				"format=24", "value_separation=false", "wal_enabled=true", "bytes_per_sync=524288",
				"wal_bytes_per_sync=0", "read_compaction_rate=16000", "read_sampling_multiplier=2",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in %q", want, got)
				}
			}
			if strings.Contains(got, "secret-datadir") {
				t.Fatalf("datadir in options log: %s", got)
			}
		})
	}
}

type nopLogCloser struct{ io.Writer }

func (nopLogCloser) Close() error { return nil }

func TestEffectiveOptionsOverrides(t *testing.T) {
	clearOptionsEnv(t)
	for name, value := range map[string]string{
		"HTND_BLOOM_FILTER_LEVEL": "15", "HTND_MEMTABLE_SIZE_MB": "32",
		"HTND_MEMTABLE_STOP_THRESHOLD": "3", "HTND_PEBBLE_CACHE_MB": "512",
		"HTND_PEBBLE_MAX_OPEN_FILES": "128", "HTND_L0_COMPACTION_THRESHOLD": "7",
		"HTND_L0_STOP_WRITES_THRESHOLD": "20", "HTND_L0_COMPACTION_FILE_THRESHOLD": "5",
		"HTND_PEBBLE_DISABLE_WAL": "true",
	} {
		t.Setenv(name, value)
	}
	opts := Options(8)
	defer opts.Cache.Unref()
	got := effectiveOptionsSummary(opts, pebble.FormatValueSeparation)
	for _, want := range []string{"bloom_bits_per_key=15", "memtable_bytes=33554432", "memtable_stop=3",
		"cache_bytes=536870912", "max_open_files=128", "l0_compaction_sublevels=7",
		"l0_stop_sublevels=20", "l0_compaction_files=5", "wal_enabled=false"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestMetricsSummary(t *testing.T) {
	m := &pebble.Metrics{}
	m.Levels[0].TablesCount, m.Levels[0].Sublevels = 17, 4
	m.Compact.EstimatedDebt = 12345
	m.MemTable.Count, m.MemTable.Size = 3, 4567
	m.MemTable.ZombieCount, m.MemTable.ZombieSize = 2, 890
	want := "l0_files=17 l0_sublevels=4 compaction_debt_bytes=12345 memtables=3 memtable_bytes=4567 zombie_memtables=2 zombie_memtable_bytes=890 write_stall_begins=9 write_stall_ends=8"
	if got := metricsSummary(m, 9, 8); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestMetricsCallbacksAndLifecycle(t *testing.T) {
	clearOptionsEnv(t)
	opts := Options(8)
	defer opts.Cache.Unref()
	var oldBegins, oldEnds int
	opts.EventListener.WriteStallBegin = func(pebble.WriteStallBeginInfo) { oldBegins++ }
	opts.EventListener.WriteStallEnd = func() { oldEnds++ }
	m := newMetricsLogger(opts)
	// No worker is running: callbacks must still return and bounded wakeups
	// must preserve all counts. Existing listeners must still be called.
	for i := 0; i < 100; i++ {
		opts.EventListener.WriteStallBegin(pebble.WriteStallBeginInfo{})
		opts.EventListener.WriteStallEnd()
	}
	if oldBegins != 100 || oldEnds != 100 {
		t.Fatal("existing callbacks lost")
	}
	reports := make(chan string, 10)
	go m.run(func() *pebble.Metrics { return &pebble.Metrics{} }, time.Hour,
		func(event, summary string) { reports <- event + " " + summary })
	defer m.close()
	select {
	case got := <-reports:
		if !strings.Contains(got, "write-stall-begin/end") || !strings.Contains(got, "write_stall_begins=100 write_stall_ends=100") {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no stall report")
	}
	opts.EventListener.WriteStallBegin(pebble.WriteStallBeginInfo{})
	select {
	case got := <-reports:
		if !strings.HasPrefix(got, "write-stall-begin ") {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no begin report")
	}
	opts.EventListener.WriteStallEnd()
	select {
	case got := <-reports:
		if !strings.HasPrefix(got, "write-stall-end ") {
			t.Fatal(got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end report")
	}
}

func TestMetricsCloseWaitsForSample(t *testing.T) {
	clearOptionsEnv(t)
	opts := Options(8)
	defer opts.Cache.Unref()
	m := newMetricsLogger(opts)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var reports []string
	go m.run(func() *pebble.Metrics {
		once.Do(func() { close(entered) })
		<-release
		return &pebble.Metrics{}
	}, time.Millisecond, func(event, summary string) { reports = append(reports, event+" "+summary) })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		m.close()
		t.Fatal("no periodic sample")
	}
	// Metrics is blocked (as if waiting for Pebble's lock); callbacks still
	// return without waiting for it, even when the wakeup buffer is full.
	for i := 0; i < 100; i++ {
		opts.EventListener.WriteStallBegin(pebble.WriteStallBeginInfo{})
		opts.EventListener.WriteStallEnd()
	}
	closed := make(chan struct{})
	go func() { m.close(); close(closed) }()
	<-m.stop
	select {
	case <-closed:
		t.Fatal("close returned while metrics still running")
	default:
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close did not join metrics worker")
	}
	if len(reports) == 0 || !strings.Contains(reports[len(reports)-1], "write_stall_begins=100 write_stall_ends=100") {
		t.Fatalf("pending stall counts lost on close: %v", reports)
	}
	// Late notifications do not block or touch the closed DB.
	opts.EventListener.WriteStallBegin(pebble.WriteStallBeginInfo{})
	opts.EventListener.WriteStallEnd()
}
