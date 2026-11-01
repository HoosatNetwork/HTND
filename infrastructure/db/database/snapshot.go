package database

import "errors"

var (
	ErrSnapshotClosed      = errors.New("snapshot is closed")
	ErrSnapshotCursorsOpen = errors.New("snapshot still has open cursors")
)

// Snapshot is a stable, process-local read view. Get returns caller-owned bytes;
// cursors follow Cursor's borrowed-byte contract. Reopening a cursor observes the
// same view, including after live writes or deletes.
//
// One owner serializes snapshot/cursor use and closes all cursors before Close.
// Close refuses open cursors without releasing the view. After successful Close,
// reads and repeated Close return ErrSnapshotClosed. The database must outlive the
// view. Snapshots are not durable recovery inputs and make no WAL/admission promise.
type Snapshot interface {
	Get(*Key) ([]byte, error)
	Has(*Key) (bool, error)
	Cursor(*Bucket) (Cursor, error)
	Close() error
}

// Snapshotter is optional. Callers must handle unsupported backends explicitly;
// the ordinary database interface and synchronous path remain available.
type Snapshotter interface{ NewSnapshot() (Snapshot, error) }
