package pebble

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
)

const (
	commitBatchCount   = 200
	commitBatchPuts    = 1000
	commitBatchValueSz = 200
	// The unfixed code allocates about 690-830 KB per commit, the fixed code about 220 KB.
	commitBatchMaxBytesPerCommit = 400 * 1024
)

// raceEnabled reports whether the test binary was built with -race. The race detector makes
// sync.Pool drop items at random, so the buffer reuse this test measures is not reliable there.
func raceEnabled() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, setting := range info.Settings {
		if setting.Key == "-race" {
			return setting.Value == "true"
		}
	}
	return false
}

// TestCommitReleasesBatchBuffer commits many large batches and checks that the bytes allocated
// per commit stay low, which only holds if a committed batch is closed so that its buffer is
// reused instead of left for the garbage collector.
func TestCommitReleasesBatchBuffer(t *testing.T) {
	for _, indexed := range []bool{true, false} {
		t.Run(fmt.Sprintf("indexed=%t", indexed), func(t *testing.T) {
			ldb, err := NewPebbleDB(t.TempDir(), 8)
			if err != nil {
				t.Fatalf("NewPebbleDB: %+v", err)
			}
			t.Cleanup(func() {
				if err := ldb.Close(); err != nil {
					t.Errorf("Close: %+v", err)
				}
			})

			bucket := database.MakeBucket([]byte("bucket"))
			value := []byte(strings.Repeat("v", commitBatchValueSz))
			begin := ldb.BeginUnindexed
			if indexed {
				begin = ldb.Begin
			}

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			for i := 0; i < commitBatchCount; i++ {
				tx, err := begin()
				if err != nil {
					t.Fatalf("Begin: %+v", err)
				}
				for j := 0; j < commitBatchPuts; j++ {
					key := bucket.Key([]byte(fmt.Sprintf("%04d-%04d", i, j)))
					if err := tx.Put(key, value); err != nil {
						t.Fatalf("Put: %+v", err)
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatalf("Commit: %+v", err)
				}
				if err := tx.Rollback(); err == nil || !strings.Contains(err.Error(), "closed transaction") {
					t.Fatalf("Rollback after Commit: want a closed transaction error, got %v", err)
				}
				if err := tx.RollbackUnlessClosed(); err != nil {
					t.Fatalf("RollbackUnlessClosed after Commit: %+v", err)
				}
			}
			runtime.ReadMemStats(&after)

			perCommit := (after.TotalAlloc - before.TotalAlloc) / commitBatchCount
			t.Logf("indexed=%t: %d bytes allocated per commit", indexed, perCommit)
			if perCommit > commitBatchMaxBytesPerCommit && !raceEnabled() {
				t.Fatalf("%d bytes allocated per commit, want at most %d", perCommit, commitBatchMaxBytesPerCommit)
			}

			for i := 0; i < commitBatchCount; i++ {
				for j := 0; j < commitBatchPuts; j++ {
					got, err := ldb.Get(bucket.Key([]byte(fmt.Sprintf("%04d-%04d", i, j))))
					if err != nil {
						t.Fatalf("Get %d-%d: %+v", i, j, err)
					}
					if string(got) != string(value) {
						t.Fatalf("value %d-%d does not read back", i, j)
					}
				}
			}
		})
	}
}

func BenchmarkTransactionCommit(b *testing.B) {
	for _, indexed := range []bool{true, false} {
		b.Run(fmt.Sprintf("indexed=%t", indexed), func(b *testing.B) {
			path := b.TempDir()
			ldb, err := NewPebbleDB(path, 8)
			if err != nil {
				b.Fatalf("NewPebbleDB: %+v", err)
			}
			defer ldb.Close()
			bucket := database.MakeBucket([]byte("bucket"))
			value := []byte(strings.Repeat("v", commitBatchValueSz))
			begin := ldb.BeginUnindexed
			if indexed {
				begin = ldb.Begin
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tx, err := begin()
				if err != nil {
					b.Fatalf("Begin: %+v", err)
				}
				for j := 0; j < commitBatchPuts; j++ {
					if err := tx.Put(bucket.Key([]byte(fmt.Sprintf("%08d-%04d", i, j))), value); err != nil {
						b.Fatalf("Put: %+v", err)
					}
				}
				if err := tx.Commit(); err != nil {
					b.Fatalf("Commit: %+v", err)
				}
			}
		})
	}
}
