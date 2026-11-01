package pruningstore

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

type journalCancelDigestDB struct {
	model.DBManager
	records               model.DBBucket
	cancel                context.CancelFunc
	reads, opened, closed int
}

func (d *journalCancelDigestDB) AvailableDiskBytes() (uint64, error) {
	return d.DBManager.(interface{ AvailableDiskBytes() (uint64, error) }).AvailableDiskBytes()
}

func (d *journalCancelDigestDB) Cursor(bucket model.DBBucket) (model.DBCursor, error) {
	cursor, err := d.DBManager.Cursor(bucket)
	if err != nil || !bytes.Equal(bucket.Path(), d.records.Path()) {
		return cursor, err
	}
	d.opened++
	return &journalCancelDigestCursor{cursor, d}, nil
}

type journalCancelDigestCursor struct {
	model.DBCursor
	db *journalCancelDigestDB
}

func (c *journalCancelDigestCursor) Value() ([]byte, error) {
	value, err := c.DBCursor.Value()
	c.db.reads++
	if c.db.reads == 3 {
		c.db.cancel()
	}
	return value, err
}

func (c *journalCancelDigestCursor) Close() error {
	c.db.closed++
	return c.DBCursor.Close()
}

// Cancellation during checksum verification must stop the unbounded record walk,
// close its cursor and leave the durable phase resumable. Build must not seal READY
// after cancellation; Apply must not walk the remainder before noticing shutdown.
func TestPP42JournalDigestCancellation(t *testing.T) {
	t.Setenv("HTND_TEST_MODE", "0")
	t.Setenv("HTND_PEBBLE_DISABLE_WAL", "0")
	for _, phase := range []string{"build", "apply"} {
		t.Run(phase, func(t *testing.T) {
			j, closeDB := journalOpen(t, t.TempDir())
			defer closeDB()
			var base externalapi.DomainHash
			if err := j.Initialize(0, &base); err != nil {
				t.Fatal(err)
			}
			state, err := j.Load()
			if err != nil {
				t.Fatal(err)
			}
			state, err = j.Begin(state, 1, externalapi.NewDomainHashFromByteArray(&[32]byte{1}))
			if err != nil {
				t.Fatal(err)
			}
			diff := journalFixture(32, false)
			wantPhase := "preparing"
			if phase == "apply" {
				if err = j.Build(context.Background(), state, diff, "fixture"); err != nil {
					t.Fatal(err)
				}
				wantPhase = "ready"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db := &journalCancelDigestDB{DBManager: j.DB, records: j.records, cancel: cancel}
			j.DB = db
			if phase == "build" {
				err = j.Build(ctx, state, diff, "fixture")
			} else {
				err = j.Apply(ctx, state)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled %s: %v", phase, err)
			}
			if db.reads != 3 {
				t.Errorf("checksum read %d records after cancellation at record 3", db.reads)
			}
			if db.opened != db.closed {
				t.Errorf("cursor leak: opened %d closed %d", db.opened, db.closed)
			}
			state, err = j.Load()
			if err != nil || state.Phase != wantPhase || state.Applied != 0 {
				t.Fatalf("durable phase after cancellation: %+v %v", state, err)
			}
			j.DB = db.DBManager
			if phase == "build" {
				if err = j.Build(context.Background(), state, diff, "fixture"); err != nil {
					t.Fatal(err)
				}
			}
			if err = j.Apply(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			if state.Phase != "complete" || state.Applied != 32 {
				t.Fatalf("retry: %+v", state)
			}
		})
	}
}
