//go:build pruningtest

package pruningstore

import (
	"bytes"
	"context"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// pp42CountingDB sums the internal point keys, tombstones included, that every cursor's
// iterator stepped over, read when the cursor closes.
type pp42CountingDB struct {
	model.DBManager
	points uint64
}

func (d *pp42CountingDB) Cursor(bucket model.DBBucket) (model.DBCursor, error) {
	c, e := d.DBManager.Cursor(bucket)
	if e != nil {
		return nil, e
	}
	return &pp42CountingCursor{DBCursor: c, db: d}, nil
}

type pp42CountingCursor struct {
	model.DBCursor
	db *pp42CountingDB
}

func (c *pp42CountingCursor) Close() error {
	c.db.points += c.DBCursor.(interface{ IteratorPointCount() uint64 }).IteratorPointCount()
	return c.DBCursor.Close()
}

// Journal.Clear seeks past the tombstones it wrote itself, so the internal keys its
// iterators step over are linear in the record count (each record once, plus a put and
// a tombstone per batch landing), not quadratic as when every batch restarts at First.
// A process kill mid-clear (child, WAL on) leaves a deleted prefix; the next Clear
// deletes everything left, again linearly.
func TestPP42JournalClearIsLinear(t *testing.T) {
	const batch, records = 64, 64 * 64
	saved := JournalBatchEntries
	JournalBatchEntries = batch
	defer func() { JournalBatchEntries = saved }()
	write := func(j *testJournal) {
		tx, e := j.DB.BeginUnindexed()
		if e != nil {
			t.Fatal(e)
		}
		defer tx.RollbackUnlessClosed()
		for i := 0; i < records; i++ {
			key := []byte{byte(i >> 8), byte(i)}
			if e = tx.Put(j.records.Key(key), []byte{1}); e != nil {
				t.Fatal(e)
			}
		}
		if e = journalSync(tx); e != nil {
			t.Fatal(e)
		}
	}
	if dir := os.Getenv("PP42_CLEAR_DIR"); dir != "" {
		j, _ := journalOpen(t, dir)
		write(j)
		seen := 0
		j.Hook = func(p string) {
			if p == "cleanup" {
				if seen++; seen == 3 {
					// The batches commit without sync: fence the WAL, as the consensus
					// crash child does, so the kill keeps them and lands mid-clear.
					tx, e := j.DB.BeginUnindexed()
					if e == nil {
						if e = tx.Put(j.prefix.Key([]byte("wal-fence")), []byte{1}); e == nil {
							e = journalSync(tx)
						}
					}
					if e != nil {
						panic(e)
					}
					if e := os.WriteFile(filepath.Join(dir, "barrier"), []byte(p), 0600); e != nil {
						panic(e)
					}
					select {}
				}
			}
		}
		_ = j.Clear(context.Background())
		os.Exit(0)
	}
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	// Bound: every record stepped over once live and once as put + tombstone, plus slack
	// per batch. Restarting each batch at First costs about records*records/batch.
	bound := uint64(2*records + 8*(records/batch+1))
	clearCounted := func(j *testJournal) uint64 {
		counting := &pp42CountingDB{DBManager: j.DB}
		j.DB = counting
		if e := j.Clear(context.Background()); e != nil {
			t.Fatal(e)
		}
		j.DB = counting.DBManager
		t.Logf("Clear stepped over %d internal keys (bound %d)", counting.points, bound)
		if left := pp42RecordCount(t, j); left != 0 {
			t.Fatalf("%d records left after Clear", left)
		}
		return counting.points
	}
	t.Run("full", func(t *testing.T) {
		j, closeDB := journalOpen(t, t.TempDir())
		defer closeDB()
		write(j)
		if points := clearCounted(j); points == 0 || points > bound {
			t.Fatalf("Clear of %d records stepped over %d internal keys, bound %d", records, points, bound)
		}
	})
	t.Run("killed", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command(os.Args[0], "-test.run=^TestPP42JournalClearIsLinear$", "-test.timeout=60s")
		cmd.Env = append(os.Environ(), "PP42_CLEAR_DIR="+dir)
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if e := cmd.Start(); e != nil {
			t.Fatal(e)
		}
		for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if _, e := os.Stat(filepath.Join(dir, "barrier")); e == nil {
				break
			}
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("no barrier: %s", output.String())
			}
		}
		if e := cmd.Process.Kill(); e != nil {
			t.Fatal(e)
		}
		_ = cmd.Wait()
		j, closeDB := journalOpen(t, dir)
		defer closeDB()
		left := pp42RecordCount(t, j)
		t.Logf("records left after the kill: %d of %d", left, records)
		if left == 0 || left == records {
			t.Fatalf("kill mid-clear left %d of %d records", left, records)
		}
		if points := clearCounted(j); points == 0 || points > bound+uint64(2*(records-left)) {
			t.Fatalf("Clear after the kill stepped over %d internal keys, bound %d", points, bound+uint64(2*(records-left)))
		}
	})
}
