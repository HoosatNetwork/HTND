package pruningstore

import (
	"bytes"
	"encoding/json"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
)

// All crash barriers live in this database decorator. The production journal has
// no runtime-enabled test callbacks; tagged integration tests add phase hooks.
type testJournal struct {
	*Journal
	Hook func(string)
}

func (j *testJournal) hit(phase string) {
	if j.Hook != nil {
		j.Hook(phase)
	}
}
func (j *testJournal) SetTestHook(h func(string)) { j.Hook = h }

type journalBoundaryDB struct {
	model.DBManager
	journal *testJournal
}

func (d *journalBoundaryDB) AvailableDiskBytes() (uint64, error) {
	return d.DBManager.(interface{ AvailableDiskBytes() (uint64, error) }).AvailableDiskBytes()
}
func (d *journalBoundaryDB) BeginUnindexed() (model.DBTransaction, error) {
	tx, err := d.DBManager.BeginUnindexed()
	if err != nil {
		return nil, err
	}
	return &journalBoundaryTx{DBTransaction: tx, db: d}, nil
}

type journalBoundaryTx struct {
	model.DBTransaction
	db               *journalBoundaryDB
	state            *JournalState
	records, deletes int
}

func (t *journalBoundaryTx) Put(k model.DBKey, value []byte) error {
	if bytes.Equal(k.Bytes(), t.db.journal.state.Bytes()) {
		t.state = new(JournalState)
		if err := json.Unmarshal(value, t.state); err != nil {
			return err
		}
	}
	if bytes.HasPrefix(k.Bytes(), t.db.journal.records.Path()) {
		t.records++
	}
	return t.DBTransaction.Put(k, value)
}
func (t *journalBoundaryTx) Delete(k model.DBKey) error {
	t.deletes++
	return t.DBTransaction.Delete(k)
}
func (t *journalBoundaryTx) Commit() error     { return t.commit(false) }
func (t *journalBoundaryTx) CommitSync() error { return t.commit(true) }
func (t *journalBoundaryTx) commit(sync bool) error {
	j := t.db.journal
	applying := t.state != nil && t.state.TargetIndex > t.state.BaseIndex && t.state.Applied > 0
	if applying {
		j.hit("before-data-commit")
	}
	var err error
	if sync {
		err = t.DBTransaction.(interface{ CommitSync() error }).CommitSync()
	} else {
		err = t.DBTransaction.Commit()
	}
	if err != nil {
		return err
	}
	switch {
	case applying:
		j.hit("data-commit")
		if t.state.Phase == "complete" {
			j.hit("complete")
		}
	case t.state != nil && t.state.Phase == "preparing":
		j.hit("preparing")
	case t.state != nil && t.state.Phase == "ready":
		j.hit("ready")
	case t.records > 0:
		j.hit("journal-chunk")
	case t.state == nil:
		j.hit("cleanup")
	}
	return nil
}
