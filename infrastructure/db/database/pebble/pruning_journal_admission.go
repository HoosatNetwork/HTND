package pebble

import (
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/pkg/errors"
)

// Synced journal commits depend on WAL. Reject before even legacy recovery or
// journal initialization can write; the read snapshot itself does not require WAL.
func (db *DB) CheckPruningJournalAdmission() error {
	if db.walDisabled {
		return errors.New("pruning journal: requires WAL enabled")
	}
	return nil
}

// AvailableDiskBytes reports free bytes on the database's filesystem. The journal's
// declared policy decides what is enough; this backend only measures.
func (db *DB) AvailableDiskBytes() (uint64, error) {
	usage, err := vfs.Default.GetDiskUsage(db.directory)
	if err != nil {
		return 0, errors.WithStack(err)
	}
	return usage.AvailBytes, nil
}
