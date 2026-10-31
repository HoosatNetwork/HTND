//go:build !pruningtest

package database

// The default build does not count masked reads.
type historyMaskReads struct{}

func (*historyMaskReads) count(HistoryMaskReader) {}

// TakeReads returns zeros: the default build does not count masked reads.
func (*HistoryMask) TakeReads() (reads [HistoryMaskReaders]uint64) { return reads }
