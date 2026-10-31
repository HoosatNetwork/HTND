package pruningstore

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// A partial build batch must obey the same reserve as a full batch. In particular,
// free space can disappear after Begin or after the preceding full build batch.
func TestPP42JournalPartialBuildReserve(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	for _, tail := range []bool{false, true} {
		name := "short-job"
		if tail {
			name = "tail-after-full-batch"
		}
		t.Run(name, func(t *testing.T) {
			j, closeDB := journalOpen(t, t.TempDir())
			defer closeDB()
			available := uint64(math.MaxUint64)
			j.DB = &journalFaultDB{DBManager: j.DB, avail: &available}
			var base externalapi.DomainHash
			if err := j.Initialize(0, &base); err != nil {
				t.Fatal(err)
			}
			state, err := j.Load()
			if err != nil {
				t.Fatal(err)
			}
			target := externalapi.NewDomainHashFromByteArray(&[32]byte{1})
			state, err = j.Begin(state, 1, target)
			if err != nil {
				t.Fatal(err)
			}
			records, committed := 1, 0
			if tail {
				records, committed = JournalBatchEntries+1, JournalBatchEntries
				j.SetTestHook(func(phase string) {
					if phase == "journal-chunk" {
						available = 0
					}
				})
			} else {
				available = 0
			}
			err = j.Build(context.Background(), state, journalFixture(records, false), "fixture")
			if !errors.Is(err, ErrJournalDiskReserve) {
				t.Fatalf("reserve refusal: %v", err)
			}
			if got := pp42RecordCount(t, j); got != committed {
				t.Fatalf("reserve-refused build committed %d records, want %d", got, committed)
			}
			state, err = j.Load()
			if err != nil || state.Phase != "preparing" {
				t.Fatalf("manifest: %+v %v", state, err)
			}
			available = math.MaxUint64
			j.SetTestHook(nil)
			if err = j.Build(context.Background(), state, journalFixture(records, false), "fixture"); err != nil {
				t.Fatal(err)
			}
			if state.Phase != "ready" || state.Count != uint64(records) {
				t.Fatalf("retry did not seal exact records: %+v", state)
			}
		})
	}
}
