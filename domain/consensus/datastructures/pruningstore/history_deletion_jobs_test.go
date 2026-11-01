package pruningstore

import (
	"encoding/binary"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/datastructures/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"math"
	"testing"
)

func TestHistoryJobRecoveryAndMalformedCount(t *testing.T) {
	db, prefix, closeDB := testutils.NewTestDB(t)
	defer closeDB()
	store := New(prefix, 2, false).(*pruningStore)
	for _, index := range []uint64{9, 2} {
		stage := model.NewStagingArea()
		store.StageHistoryDeletionJob(stage, &HistoryDeletionJob{Index: index, DeletionPoint: testutils.Hash(1), PrunedTips: []*externalapi.DomainHash{testutils.Hash(2)}})
		testutils.Commit(t, db, stage)
	}
	recovered := New(prefix, 2, false).(*pruningStore)
	jobs, err := recovered.HistoryDeletionJobs(db)
	if err != nil || len(jobs) != 2 || jobs[0].Index != 2 || jobs[1].Index != 9 {
		t.Fatalf("recovery: %+v %v", jobs, err)
	}
	// An overflowing count must fail before allocation, including a multiple of 2^59.
	for _, count := range []uint64{math.MaxUint64, 1 << 59} {
		malformed := append([]byte{historyDeletionJobVersion}, make([]byte, externalapi.DomainHashSize)...)
		malformed = binary.AppendUvarint(malformed, count)
		if _, err := deserializeHistoryDeletionJob(2, malformed); err == nil {
			t.Fatalf("accepted count %d", count)
		}
	}
}
