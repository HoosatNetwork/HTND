//go:build pruningtest

package database

// IteratorPointCount forwards the backend cursor's count of internal point keys
// stepped over, tombstones included; 0 when the backend does not count them.
func (d *dbCursor) IteratorPointCount() uint64 {
	if counter, ok := d.cursor.(interface{ IteratorPointCount() uint64 }); ok {
		return counter.IteratorPointCount()
	}
	return 0
}
