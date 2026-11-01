//go:build pruningtest

package pruningstore

type journalHooks struct{ Hook func(string) }

func (j *Journal) SetTestHook(h func(string)) { j.Hook = h }
func (j *Journal) TestHook(s string) {
	if j.Hook != nil {
		j.Hook(s)
	}
}
func (j *Journal) hit(s string) { j.TestHook(s) }

// JournalBatchEntries is variable only in lab builds, so consensus-level crash tests
// can split small test diffs into several build/apply batches.
var JournalBatchEntries = 4096
