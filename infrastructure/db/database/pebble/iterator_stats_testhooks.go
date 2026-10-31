//go:build pruningtest

package pebble

// IteratorPointCount is the number of internal point keys, tombstones included, this
// cursor's iterator has stepped over. Lab tests use it to bound iteration cost.
func (c *DBCursor) IteratorPointCount() uint64 {
	return c.iterator.Stats().InternalStats.PointCount
}
