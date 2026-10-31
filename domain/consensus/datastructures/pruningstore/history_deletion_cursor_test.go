package pruningstore

import (
	"errors"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
)

type failedHistoryReader struct {
	model.DBReader
	err error
}

func (r failedHistoryReader) Cursor(bucket model.DBBucket) (model.DBCursor, error) {
	cursor, err := r.DBReader.Cursor(bucket)
	if err != nil {
		return nil, err
	}
	return &failedHistoryCursor{DBCursor: cursor, err: r.err}, nil
}

type failedHistoryCursor struct {
	model.DBCursor
	err error
}

// Pebble reports iteration errors through Close after First/Next return false.
func (c *failedHistoryCursor) First() bool { return false }
func (c *failedHistoryCursor) Close() error {
	return errors.Join(c.DBCursor.Close(), c.err)
}

func TestHistoryJobsRejectFailedIteration(t *testing.T) {
	db, prefix, closeDB := testutils.NewTestDB(t)
	defer closeDB()
	store := New(prefix, 2, false).(*pruningStore)
	stage := model.NewStagingArea()
	store.StageHistoryDeletionJob(stage, &HistoryDeletionJob{Index: 1, DeletionPoint: testutils.Hash(1)})
	testutils.Commit(t, db, stage)
	injected := errors.New("history iterator failed")
	if _, err := store.HistoryDeletionJobs(failedHistoryReader{db, injected}); !errors.Is(err, injected) {
		t.Fatalf("unread deletion job reported as finished: %v", err)
	}
	jobs, err := store.HistoryDeletionJobs(db)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job must remain recoverable: %v, %v", jobs, err)
	}
}
