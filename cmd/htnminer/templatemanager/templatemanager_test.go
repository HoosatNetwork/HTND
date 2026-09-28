package templatemanager

import (
	"math/big"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
)

func templateWithParent(t *testing.T, parentByte byte, timestamp int64) *appmessage.GetBlockTemplateResponseMessage {
	t.Helper()
	parent := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{parentByte})
	header := blockheader.NewImmutableBlockHeader(
		5,
		[]externalapi.BlockLevelParents{{parent}},
		&externalapi.DomainHash{}, &externalapi.DomainHash{}, &externalapi.DomainHash{},
		timestamp, 0x207fffff, 0, 1, 1, big.NewInt(1), &externalapi.DomainHash{},
	)
	block := &externalapi.DomainBlock{Header: header, Transactions: []*externalapi.DomainTransaction{}}
	return appmessage.NewGetBlockTemplateResponseMessage(appmessage.DomainBlockToRPCBlock(block), true)
}

func reset() {
	lock.Lock()
	defer lock.Unlock()
	current, parentsKey, solvedParentsKey = nil, "", ""
}

func mustCurrent(t *testing.T) *Job {
	t.Helper()
	job, _, _ := Current()
	if job == nil {
		t.Fatalf("expected a job to mine")
	}
	return job
}

// TestSolveOncePerParents pins that a template is solved at most once until its parents change,
// so the miner never submits siblings of its own blocks.
func TestSolveOncePerParents(t *testing.T) {
	reset()
	if err := Set(templateWithParent(t, 1, 1000)); err != nil {
		t.Fatal(err)
	}
	job := mustCurrent(t)
	if job.ID != Generation() {
		t.Fatalf("job ID %d does not match generation %d", job.ID, Generation())
	}

	if !MarkSolved(job.ID) {
		t.Fatalf("first solution must be claimed")
	}
	if MarkSolved(job.ID) {
		t.Fatalf("second solution of the same job must be dropped")
	}
	if job, _, _ := Current(); job != nil {
		t.Fatalf("solved template must not be mined again")
	}

	// A rebuilt template on the same parents (e.g. a newer timestamp) is still a sibling.
	if err := Set(templateWithParent(t, 1, 2000)); err != nil {
		t.Fatal(err)
	}
	if job, _, _ := Current(); job != nil {
		t.Fatalf("template with the solved parents must not be mined")
	}

	// A template building on new parents resumes mining.
	if err := Set(templateWithParent(t, 2, 3000)); err != nil {
		t.Fatal(err)
	}
	mustCurrent(t)
}

// TestSubmitFailedResumesMining pins that a rejected block releases its parents, so a node that
// rejects our block can't stall the miner on a template whose parents never change.
func TestSubmitFailedResumesMining(t *testing.T) {
	reset()
	if err := Set(templateWithParent(t, 1, 1000)); err != nil {
		t.Fatal(err)
	}
	job := mustCurrent(t)
	if !MarkSolved(job.ID) {
		t.Fatalf("first solution must be claimed")
	}
	SubmitFailed(job.Block)
	resumed := mustCurrent(t)
	if resumed.ID != Generation() || resumed.ID == job.ID {
		t.Fatalf("resumed job must get a new current ID, got %d (old %d, generation %d)", resumed.ID, job.ID, Generation())
	}
}

// TestIdenticalTemplateKeepsGeneration pins that re-polling an unchanged template doesn't restart
// the mining threads.
func TestIdenticalTemplateKeepsGeneration(t *testing.T) {
	reset()
	if err := Set(templateWithParent(t, 1, 1000)); err != nil {
		t.Fatal(err)
	}
	before := Generation()
	if err := Set(templateWithParent(t, 1, 1000)); err != nil {
		t.Fatal(err)
	}
	if Generation() != before {
		t.Fatalf("identical template bumped the generation from %d to %d", before, Generation())
	}
}
