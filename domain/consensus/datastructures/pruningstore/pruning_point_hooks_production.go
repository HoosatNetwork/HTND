//go:build !pruningtest

package pruningstore

type journalHooks struct{}

func (*Journal) SetTestHook(func(string)) {}
func (*Journal) TestHook(string)          {}
func (*Journal) hit(string)               {}

const JournalBatchEntries = 4096
