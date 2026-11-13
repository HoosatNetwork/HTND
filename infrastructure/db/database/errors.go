package database

import "errors"

// ErrNotFound denotes that the requested item was not
// found in the database.
var ErrNotFound = errors.New("not found")

// IsNotFoundError checks whether an error is an ErrNotFound.
func IsNotFoundError(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// ErrJournalRequiresWAL denotes that the pruning point UTXO journal cannot be used
// because the database runs with its write-ahead log disabled.
var ErrJournalRequiresWAL = errors.New("pruning journal: requires WAL enabled")
