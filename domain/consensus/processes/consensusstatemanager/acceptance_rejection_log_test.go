package consensusstatemanager

import (
	"strings"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
)

func outpointsForTest(count int) []*externalapi.DomainOutpoint {
	outpoints := make([]*externalapi.DomainOutpoint, 0, count)
	for i := 0; i < count; i++ {
		id := externalapi.NewDomainTransactionIDFromByteArray(
			&[externalapi.DomainHashSize]byte{byte(i + 1)})
		outpoints = append(outpoints, externalapi.NewDomainOutpoint(id, uint32(i)))
	}
	return outpoints
}

// TestAcceptanceRejectionIgnoresWholeTransactionMisses pins the discriminator that keeps this
// diagnostic honest.
//
// On a DAG the same transaction is carried by several blocks and only the first merge accepts it.
// Every later copy is refused because all of its inputs are already consumed - correct, constant,
// and nothing to do with this node's health. The first version of this check reported those, and
// live data proved it: of the three transactions it named, two were confirmed by scanning
// acceptance data to have been accepted earlier at chain indexes 114140 and 114299. All three had
// every input missing.
func TestAcceptanceRejectionIgnoresWholeTransactionMisses(t *testing.T) {
	// Start the throttle window now, so the calls accumulate into the counter instead of being
	// reported and reset immediately - the report is not what is under test here.
	csm := &consensusStateManager{missingInputLastReport: time.Now()}
	blockHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{9})
	transactionID := externalapi.NewDomainTransactionIDFromByteArray(
		&[externalapi.DomainHashSize]byte{8})

	// Every one of the 88 inputs missing: an already-accepted transaction, not a gap.
	err := ruleerrors.NewErrMissingTxOut(outpointsForTest(88))
	csm.noteAcceptanceRejection(blockHash, transactionID, 88, err)
	if csm.missingInputRejections != 0 {
		t.Errorf("a transaction with every input missing must not be counted as a gap, got %d",
			csm.missingInputRejections)
	}

	// Three of 88 missing: this node is short some particular coins.
	err = ruleerrors.NewErrMissingTxOut(outpointsForTest(3))
	csm.noteAcceptanceRejection(blockHash, transactionID, 88, err)
	if csm.missingInputRejections != 1 {
		t.Errorf("a partial miss is the shape of a real gap and must be counted, got %d",
			csm.missingInputRejections)
	}
	if csm.missingInputExampleInputs != 88 || csm.missingInputExampleCount != 3 {
		t.Errorf("the example must record both counts, got %d of %d",
			csm.missingInputExampleCount, csm.missingInputExampleInputs)
	}
}

// TestAcceptanceRejectionIgnoresDoubleSpends keeps the other normal case out. A double spend
// detected inside the accumulated diff is ordinary duplicate handling.
func TestAcceptanceRejectionIgnoresDoubleSpends(t *testing.T) {
	csm := &consensusStateManager{missingInputLastReport: time.Now()}
	blockHash := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{9})
	transactionID := externalapi.NewDomainTransactionIDFromByteArray(
		&[externalapi.DomainHashSize]byte{8})

	missing := outpointsForTest(2)
	err := ruleerrors.NewErrMissingOrSpentTxOut(missing, missing)
	csm.noteAcceptanceRejection(blockHash, transactionID, 88, err)
	if csm.missingInputRejections != 0 {
		t.Errorf("a double spend must not be counted as a missing coin, got %d",
			csm.missingInputRejections)
	}
}

// TestAcceptanceRejectionSurvivesNils - a diagnostic must never be the thing that brings acceptance
// down.
func TestAcceptanceRejectionSurvivesNils(t *testing.T) {
	csm := &consensusStateManager{missingInputLastReport: time.Now()}
	err := ruleerrors.NewErrMissingTxOut(outpointsForTest(1))
	csm.noteAcceptanceRejection(nil, nil, 88, err)
	csm.noteAcceptanceRejection(nil, nil, 0, nil)
	if csm.missingInputRejections != 0 {
		t.Error("nil inputs must be ignored, not counted")
	}
}

func TestAcceptanceRejectionMessageDoesNotOverclaim(t *testing.T) {
	// The wording matters: the first version asserted the node "does not hold coins they spend",
	// which was untrue for the duplicate case it was firing on.
	source := acceptanceRejectionMessageForTest()
	if strings.Contains(source, "does not hold coins they spend") {
		t.Error("the message must not assert a gap it has not established")
	}
}
