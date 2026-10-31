package pruningstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/utxo"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/pebble"
	"github.com/HoosatNetwork/HTND/v2/util/staging"
)

func journalFixture(n int, restamp bool) externalapi.UTXODiff {
	add, remove := map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}, map[externalapi.DomainOutpoint]externalapi.UTXOEntry{}
	for i := 0; i < n; i++ {
		out := externalapi.DomainOutpoint{Index: uint32(i)}
		old := utxo.NewUTXOEntry(uint64(i+1), &externalapi.ScriptPublicKey{Script: []byte{0x51}}, true, 1)
		if restamp {
			remove[out] = old
			if i%3 == 0 {
				continue
			}
		}
		score := uint64(1)
		if restamp {
			score = 2
		}
		add[out] = utxo.NewUTXOEntry(uint64(i+1), &externalapi.ScriptPublicKey{Script: []byte{0x51}}, true, score)
	}
	diff, e := utxo.NewUTXODiffFromCollections(utxo.NewUTXOCollection(add), utxo.NewUTXOCollection(remove))
	if e != nil {
		panic(e)
	}
	return diff
}
func journalOpen(t testing.TB, path string) (*testJournal, func()) {
	t.Helper()
	raw, e := pebble.OpenPebbleDB(path, 8)
	if e != nil {
		t.Fatal(e)
	}
	db := database.New(raw)
	prefix := database.MakeBucket([]byte("test"))
	ps := New(prefix, 2, false)
	j := &testJournal{Journal: NewJournal(db, prefix, ps)}
	j.DB = &journalBoundaryDB{DBManager: db, journal: j}
	return j, func() {
		if e := raw.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
func journalRun(t testing.TB, j *testJournal, n int) {
	t.Helper()
	ctx := context.Background()
	s, e := j.Load()
	if e != nil {
		t.Fatal(e)
	}
	var base, target externalapi.DomainHash
	target = *externalapi.NewDomainHashFromByteArray(&[32]byte{1})
	if s == nil {
		if e = j.Store.UpdatePruningPointUTXOSet(j.DB, journalFixture(n, false)); e != nil {
			t.Fatal(e)
		}
		stage := model.NewStagingArea()
		j.Store.StageStartUpdatingPruningPointUTXOSet(stage)
		if e = staging.CommitAllChanges(j.DB, stage); e != nil {
			t.Fatal(e)
		}
		if e = j.Initialize(0, &base); e != nil {
			t.Fatal(e)
		}
		s, e = j.Load()
		if e != nil {
			t.Fatal(e)
		}
	}
	if s.TargetIndex == 0 {
		s, e = j.Begin(s, 1, &target)
		if e != nil {
			t.Fatal(e)
		}
	}
	if s.Phase == "preparing" {
		if e = j.Build(ctx, s, journalFixture(n, true), "fixture"); e != nil {
			t.Fatal(e)
		}
	}
	if s.Phase == "ready" {
		if e = j.Apply(ctx, s); e != nil {
			t.Fatal(e)
		}
	}
	if s.Phase != "complete" {
		t.Fatal(s.Phase)
	}
	// Simulate the existing atomic finish boundary; no deletion is moved out of it.
	stage := model.NewStagingArea()
	j.Store.StageFinishUpdatingPruningPointUTXOSet(stage)
	j.hit("before-finish")
	if e = staging.CommitAllChanges(j.DB, stage); e != nil {
		t.Fatal(e)
	}
	j.hit("finish")
}
func journalBytes(t testing.TB, j *testJournal) [][]byte {
	t.Helper()
	c, e := j.DB.Cursor(j.live)
	if e != nil {
		t.Fatal(e)
	}
	var result [][]byte
	for ok := c.First(); ok; ok = c.Next() {
		k, e := c.Key()
		if e != nil {
			t.Fatal(e)
		}
		v, e := c.Value()
		if e != nil {
			t.Fatal(e)
		}
		result = append(result, append(append([]byte(nil), k.Suffix()...), v...))
	}
	if e = c.Close(); e != nil {
		t.Fatal(e)
	}
	return result
}
func journalCompareLegacy(t testing.TB, j *testJournal, n int) {
	t.Helper()
	legacy, closeDB := journalOpen(t, t.TempDir())
	defer closeDB()
	for _, restamp := range []bool{false, true} {
		if e := legacy.Store.UpdatePruningPointUTXOSet(legacy.DB, journalFixture(n, restamp)); e != nil {
			t.Fatal(e)
		}
	}
	got, want := journalBytes(t, j), journalBytes(t, legacy)
	if len(got) != len(want) {
		t.Fatalf("%d entries != legacy %d", len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("entry %d differs from legacy", i)
		}
	}
}
func TestPP42JournalDifferential(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	for _, n := range []int{0, 1, 9000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			j, closeDB := journalOpen(t, t.TempDir())
			defer closeDB()
			journalRun(t, j, n)
			journalCompareLegacy(t, j, n)
		})
	}
}

// Parent kills the child after a real WAL-enabled boundary. No Close/flush runs in
// the child. Every recovered key/value is compared with the unchanged legacy writer.
func TestPP42JournalCrash(t *testing.T) {
	if dir := os.Getenv("PP42_CRASH_DIR"); dir != "" {
		j, _ := journalOpen(t, dir)
		point := os.Getenv("PP42_CRASH_POINT")
		nth := 1
		if parts := strings.Split(point, ":"); len(parts) == 2 {
			point = parts[0]
			nth, _ = strconv.Atoi(parts[1])
		}
		seen := 0
		j.Hook = func(p string) {
			if p == point {
				seen++
				if seen == nth {
					if e := os.WriteFile(filepath.Join(dir, "barrier"), []byte(p), 0600); e != nil {
						panic(e)
					}
					select {}
				}
			}
		}
		journalRun(t, j, 9000)
		os.Exit(0)
	}
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	for _, point := range []string{"preparing", "cleanup", "journal-chunk", "journal-chunk:2", "journal-chunk:3", "ready", "before-data-commit", "data-commit", "before-data-commit:2", "data-commit:2", "before-data-commit:3", "data-commit:3", "complete", "before-finish", "finish"} {
		t.Run(point, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestPP42JournalCrash$", "-test.timeout=60s")
			cmd.Env = append(os.Environ(), "PP42_CRASH_DIR="+dir, "PP42_CRASH_POINT="+point)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if e := cmd.Start(); e != nil {
				t.Fatal(e)
			}
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, e := os.Stat(filepath.Join(dir, "barrier")); e == nil {
					break
				}
				if time.Now().After(deadline) {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatalf("no barrier: %s", output.String())
				}
				time.Sleep(5 * time.Millisecond)
			}
			if e := cmd.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			_ = cmd.Wait()
			j, closeDB := journalOpen(t, dir)
			defer closeDB()
			journalRun(t, j, 9000)
			journalCompareLegacy(t, j, 9000)
		})
	}
}
func TestPP42JournalRejectsDamagedRedo(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	j, closeDB := journalOpen(t, t.TempDir())
	defer closeDB()
	var h externalapi.DomainHash
	if e := j.Initialize(0, &h); e != nil {
		t.Fatal(e)
	}
	s, _ := j.Load()
	h = *externalapi.NewDomainHashFromByteArray(&[32]byte{1})
	s, e := j.Begin(s, 1, &h)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.Build(context.Background(), s, journalFixture(5, false), "test"); e != nil {
		t.Fatal(e)
	}
	if e = j.DB.Put(j.records.Key([]byte("corrupt")), []byte{1, 2}); e != nil {
		t.Fatal(e)
	}
	if e = j.Apply(context.Background(), s); e == nil {
		t.Fatal("damaged redo accepted")
	}
	if got := journalBytes(t, j); len(got) != 0 {
		t.Fatal("damaged journal modified destination")
	}
}

// Server-only benchmark. Seeding is streamed and excluded from timing; the timed
// work includes diff serialization, journal sealing and bounded application. The
// delta is 1% by default, so the 20.7 M fixture does not require a full-set Go map.
func BenchmarkPP42Synthetic(b *testing.B) {
	n := 1270000
	if value := os.Getenv("PP42_BENCH_ENTRIES"); value != "" {
		var e error
		n, e = strconv.Atoi(value)
		if e != nil || n < 1 {
			b.Fatal("invalid entry count")
		}
	}
	delta := n / 100
	if delta < 1 {
		delta = 1
	}
	if value := os.Getenv("PP42_BENCH_DELTA"); value != "" {
		var e error
		delta, e = strconv.Atoi(value)
		if e != nil || delta < 0 || delta > n {
			b.Fatal("invalid delta")
		}
	}
	for _, method := range []string{"legacy", "journal"} {
		b.Run(method, func(b *testing.B) {
			b.ReportAllocs()
			b.ReportMetric(float64(n), "entries/op")
			b.ReportMetric(float64(delta), "delta/op")
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				j, closeDB := journalOpen(b, filepath.Join(b.TempDir(), fmt.Sprint(i)))
				seedJournalSet(b, j, n)
				diff := journalFixture(delta, true)
				var hash externalapi.DomainHash
				if e := j.Initialize(0, &hash); e != nil {
					b.Fatal(e)
				}
				s, e := j.Load()
				if e != nil {
					b.Fatal(e)
				}
				hash = *externalapi.NewDomainHashFromByteArray(&[32]byte{1})
				s, e = j.Begin(s, 1, &hash)
				if e != nil {
					b.Fatal(e)
				}
				b.StartTimer()
				if method == "legacy" {
					e = j.Store.UpdatePruningPointUTXOSet(j.DB, diff)
				} else {
					e = j.Build(context.Background(), s, diff, "synthetic")
					if e == nil {
						e = j.Apply(context.Background(), s)
					}
				}
				if e != nil {
					b.Fatal(e)
				}
				b.StopTimer()
				closeDB()
			}
		})
	}
}
func seedJournalSet(t testing.TB, j *testJournal, n int) {
	t.Helper()
	for start := 0; start < n; start += JournalBatchEntries {
		if e := j.CheckReserve(0); e != nil {
			t.Fatal(e)
		}
		tx, e := j.DB.BeginUnindexed()
		if e != nil {
			t.Fatal(e)
		}
		for i := start; i < n && i < start+JournalBatchEntries; i++ {
			out := externalapi.DomainOutpoint{Index: uint32(i)}
			entry := utxo.NewUTXOEntry(uint64(i+1), &externalapi.ScriptPublicKey{Script: []byte{0x51}}, true, 1)
			k, e := serializeOutpoint(&out)
			if e != nil {
				t.Fatal(e)
			}
			v, e := serializeUTXOEntry(entry)
			if e != nil {
				t.Fatal(e)
			}
			if e = tx.Put(j.live.Key(k), v); e != nil {
				t.Fatal(e)
			}
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		tx.RollbackUnlessClosed()
	}
}

// A failed Commit may have taken effect. Recovery must reload the durable cursor;
// it must never choose a different method against the partially changed live set.
type journalFaultDB struct {
	model.DBManager
	fail, after, reserve bool
	// avail, when set, replaces the filesystem's free-space answer.
	avail *uint64
}

func (d *journalFaultDB) AvailableDiskBytes() (uint64, error) {
	if d.reserve {
		return 0, nil
	}
	if d.avail != nil {
		return *d.avail, nil
	}
	return d.DBManager.(interface{ AvailableDiskBytes() (uint64, error) }).AvailableDiskBytes()
}
func (d *journalFaultDB) BeginUnindexed() (model.DBTransaction, error) {
	tx, e := d.DBManager.BeginUnindexed()
	if e != nil {
		return nil, e
	}
	return &journalFaultTx{tx, d}, nil
}

type journalFaultTx struct {
	model.DBTransaction
	owner *journalFaultDB
}

func (t *journalFaultTx) Commit() error {
	if !t.owner.fail {
		return t.DBTransaction.Commit()
	}
	t.owner.fail = false
	if t.owner.after {
		if e := t.DBTransaction.Commit(); e != nil {
			return e
		}
	}
	return fmt.Errorf("injected uncertain commit")
}
func TestPP42JournalCommitAndSpaceErrors(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	for _, mode := range []string{"before", "after", "reserve"} {
		t.Run(mode, func(t *testing.T) {
			j, closeDB := journalOpen(t, t.TempDir())
			defer closeDB()
			var h externalapi.DomainHash
			if e := j.Store.UpdatePruningPointUTXOSet(j.DB, journalFixture(9000, false)); e != nil {
				t.Fatal(e)
			}
			if e := j.Initialize(0, &h); e != nil {
				t.Fatal(e)
			}
			s, _ := j.Load()
			h = *externalapi.NewDomainHashFromByteArray(&[32]byte{1})
			s, e := j.Begin(s, 1, &h)
			if e != nil {
				t.Fatal(e)
			}
			if e = j.Build(context.Background(), s, journalFixture(9000, true), "fixture"); e != nil {
				t.Fatal(e)
			}
			fault := &journalFaultDB{DBManager: j.DB, fail: mode != "reserve", after: mode == "after", reserve: mode == "reserve"}
			j.DB = fault
			if e = j.Apply(context.Background(), s); e == nil {
				t.Fatal("fault not propagated")
			}
			if mode == "reserve" && !errors.Is(e, ErrJournalDiskReserve) {
				t.Fatalf("reserve refusal untyped: %v", e)
			}
			fault.reserve = false
			s, e = j.Load()
			if e != nil {
				t.Fatal(e)
			}
			if e = j.Apply(context.Background(), s); e != nil {
				t.Fatal(e)
			}
			journalCompareLegacy(t, j, 9000)
		})
	}
}

func (t *journalFaultTx) CommitSync() error {
	return t.DBTransaction.(interface{ CommitSync() error }).CommitSync()
}

// The declared policy is Floor + Margin x estimated remaining job bytes. Each refusal
// happens before the guarded write: the manifest phase and the live set are unchanged,
// and the same job continues once space is available.
func TestPP42JournalDiskPolicy(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	p := JournalDiskPolicy{}.WithDefaults()
	if p.Floor != 1<<30 || p.Margin != 4 || p.SealedFloor != 1<<30 || p.Required(10) != 1<<30+40 || p.Required(math.MaxUint64/2) != math.MaxUint64 {
		t.Fatalf("policy arithmetic %+v", p)
	}
	j, closeDB := journalOpen(t, t.TempDir())
	defer closeDB()
	var avail uint64 = math.MaxUint64
	fault := &journalFaultDB{DBManager: j.DB, avail: &avail}
	j.DB = fault
	j.Policy = JournalDiskPolicy{Floor: 1 << 20, Margin: 4, SealedFloor: 1 << 20}
	if e := j.Store.UpdatePruningPointUTXOSet(j.DB, journalFixture(9000, false)); e != nil {
		t.Fatal(e)
	}
	before := journalBytes(t, j)
	var h externalapi.DomainHash
	avail = j.Policy.Floor - 1
	if e := j.Initialize(0, &h); !errors.Is(e, ErrJournalDiskReserve) {
		t.Fatalf("floor not enforced: %v", e)
	}
	if s, e := j.Load(); e != nil || s != nil {
		t.Fatalf("refused initialization wrote a manifest: %v %v", s, e)
	}
	avail = j.Policy.Floor
	if e := j.Initialize(0, &h); e != nil {
		t.Fatal(e)
	}
	s, _ := j.Load()
	h = *externalapi.NewDomainHashFromByteArray(&[32]byte{1})
	avail = math.MaxUint64
	s, e := j.Begin(s, 1, &h)
	if e != nil {
		t.Fatal(e)
	}
	if e = j.Build(context.Background(), s, journalFixture(9000, true), "fixture"); e != nil {
		t.Fatal(e)
	}
	if s.Bytes == 0 || s.Remaining() != s.Bytes {
		t.Fatalf("sealed size not measured: %+v", s)
	}
	size := s.Bytes
	// Below the sealed floor a sealed job waits: nothing applied, manifest unchanged.
	avail = j.Policy.SealedFloor - 1
	if e = j.Apply(context.Background(), s); !errors.Is(e, ErrJournalDiskReserve) {
		t.Fatalf("sealed floor not enforced: %v", e)
	}
	if got, _ := j.Load(); got.Phase != "ready" || got.Applied != 0 {
		t.Fatalf("refused apply changed manifest: %+v", got)
	}
	if got := journalBytes(t, j); len(got) != len(before) {
		t.Fatal("refused apply changed the live set")
	}
	// An archival node deletes no history: its sealed job keeps the full requirement.
	avail = j.Policy.Required(size) - 1
	j.Archival = true
	if e = j.Apply(context.Background(), s); !errors.Is(e, ErrJournalDiskReserve) {
		t.Fatalf("archival sealed job assumed history deletion: %v", e)
	}
	if got, _ := j.Load(); got.Phase != "ready" || got.Applied != 0 {
		t.Fatalf("refused archival apply changed manifest: %+v", got)
	}
	j.Archival = false
	// At a level that refuses unsealed work for this size, the sealed job proceeds
	// (the next job's Begin below is refused at this same level).
	if e = j.Apply(context.Background(), s); e != nil {
		t.Fatalf("sealed job refused above its floor: %v", e)
	}
	journalCompareLegacy(t, j, 9000)
	// The next job is estimated from the previous measured job before PREPARING.
	s, _ = j.Load()
	if s.Phase != "complete" || s.Bytes != size {
		t.Fatalf("complete manifest lost job size: %+v", s)
	}
	avail = j.Policy.Required(size) - 1
	h2 := *externalapi.NewDomainHashFromByteArray(&[32]byte{2})
	if _, e = j.Begin(s, 2, &h2); !errors.Is(e, ErrJournalDiskReserve) {
		t.Fatalf("next-job estimate not enforced: %v", e)
	}
	if got, _ := j.Load(); got.Phase != "complete" || got.TargetIndex != 1 {
		t.Fatalf("refused begin changed manifest: %+v", got)
	}
}

func pp42RecordCount(t *testing.T, j *testJournal) int {
	t.Helper()
	c, e := j.DB.Cursor(j.records)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	n := 0
	for ok := c.First(); ok; ok = c.Next() {
		n++
	}
	return n
}
