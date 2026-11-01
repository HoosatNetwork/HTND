package database

import "github.com/pkg/errors"

// Candidate admission is separate from the generic process-local read view.
func (d *dbManager) CheckPruningJournalAdmission() error {
	checker, ok := d.db.(interface{ CheckPruningJournalAdmission() error })
	if !ok {
		return errors.New("pruning journal: backend has no durable journal admission")
	}
	return checker.CheckPruningJournalAdmission()
}

// AvailableDiskBytes feeds the journal's declared disk policy.
func (d *dbManager) AvailableDiskBytes() (uint64, error) {
	query, ok := d.db.(interface{ AvailableDiskBytes() (uint64, error) })
	if !ok {
		return 0, errors.New("pruning journal: backend has no disk space query")
	}
	return query.AvailableDiskBytes()
}
