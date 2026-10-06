package consensusstatemanager

import (
	"fmt"
	"strings"
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// [ACCEPT-STATE] journal - diagnostic only, never affects a verdict.
//
// maybeAcceptTransaction decides whether a transaction with an input this view cannot find is
// accepted (outputs created, resolved inputs spent) or rejected. That decision is made by
// acceptDespiteMissingInputs from blockInheritsKnownUTXOCommitmentOffset, which reads LOCAL state:
// whether this node's pruning point multiset matches its header, an in-memory boundary marker that
// does not survive a restart, whether the selected parent is the pruning point, and whether this
// node's stored multiset for the selected parent matches the selected parent's header. Two nodes
// running identical code can therefore reach different verdicts for the same transaction in the same
// merging block, and from then on hold different UTXO sets and commit different multisets.
//
// This line records every such verdict on a real merging block together with each input of that
// decision, so the logs of two nodes can be diffed on the key "block=<merging block> tx=<txid>" to
// find the exact transaction where they parted. Virtual (all-0xff) is skipped: it is recomputed on
// every tip change and is not committed to by any header. Each (block, tx) pair is logged once per
// process.

var (
	acceptStateJournalMutex sync.Mutex
	acceptStateJournalSeen  = make(map[string]struct{})
)

const acceptStateJournalMaxSeen = 200000

type offsetDecisionInputs struct {
	baselineChecked              bool
	baselineVerified             bool
	boundaryConfirmed            bool
	selectedParent               *externalapi.DomainHash
	selectedParentIsPruningPoint bool
	selectedParentMultiset       *externalapi.DomainHash
	selectedParentCommitment     *externalapi.DomainHash
}

// explainInheritsOffset recomputes, without side effects beyond UTXOSetHealth's own once-per-pruning-
// point memo, the individual signals blockInheritsKnownUTXOCommitmentOffset combines.
func (csm *consensusStateManager) explainInheritsOffset(stagingArea *model.StagingArea,
	blockHash *externalapi.DomainHash,
) offsetDecisionInputs {
	var inputs offsetDecisionInputs
	health := csm.UTXOSetHealth(stagingArea)
	inputs.baselineChecked = health.Checked
	inputs.baselineVerified = health.BaselineVerified
	pruningPoint := csm.currentPruningPoint(stagingArea)
	inputs.boundaryConfirmed = csm.boundaryOffsetConfirmedPruningPoint != nil && pruningPoint != nil &&
		csm.boundaryOffsetConfirmedPruningPoint.Equal(pruningPoint)

	ghostdagData, err := csm.ghostdagDataStore.Get(csm.databaseContext, stagingArea, blockHash, false)
	if err != nil || ghostdagData == nil {
		return inputs
	}
	inputs.selectedParent = ghostdagData.SelectedParent()
	if inputs.selectedParent == nil {
		return inputs
	}
	inputs.selectedParentIsPruningPoint = pruningPoint != nil && inputs.selectedParent.Equal(pruningPoint)
	if selectedParentMultiset, err := csm.multisetStore.Get(csm.databaseContext, stagingArea, inputs.selectedParent); err == nil {
		inputs.selectedParentMultiset = selectedParentMultiset.Hash()
	}
	if header, err := csm.blockHeaderStore.BlockHeader(csm.databaseContext, stagingArea, inputs.selectedParent); err == nil {
		inputs.selectedParentCommitment = header.UTXOCommitment()
	}
	return inputs
}

func (inputs offsetDecisionInputs) String() string {
	selectedParentMatches := "unknown"
	if inputs.selectedParentMultiset != nil && inputs.selectedParentCommitment != nil {
		selectedParentMatches = fmt.Sprintf("%t", inputs.selectedParentMultiset.Equal(inputs.selectedParentCommitment))
	}
	return fmt.Sprintf("baselineChecked=%t baselineVerified=%t boundaryConfirmed=%t selectedParent=%v "+
		"selectedParentIsPruningPoint=%t selectedParentMultiset=%v selectedParentHeaderCommitment=%v "+
		"selectedParentMultisetMatchesHeader=%s",
		inputs.baselineChecked, inputs.baselineVerified, inputs.boundaryConfirmed, inputs.selectedParent,
		inputs.selectedParentIsPruningPoint, inputs.selectedParentMultiset, inputs.selectedParentCommitment,
		selectedParentMatches)
}

// journalMissingInputVerdict writes one [ACCEPT-STATE] line for a missing-input verdict. accepted is
// the verdict actually taken; inheritsOffset is the value acceptDespiteMissingInputs was given.
func (csm *consensusStateManager) journalMissingInputVerdict(stagingArea *model.StagingArea,
	blockHash, mergeSetBlockHash *externalapi.DomainHash, transaction *externalapi.DomainTransaction,
	transactionID string, accepted, inheritsOffset bool, resolvedInputs int,
) {
	if blockHash == nil || blockHash.Equal(model.VirtualBlockHash) {
		return
	}
	key := blockHash.String() + ":" + transactionID
	acceptStateJournalMutex.Lock()
	if _, seen := acceptStateJournalSeen[key]; seen {
		acceptStateJournalMutex.Unlock()
		return
	}
	if len(acceptStateJournalSeen) >= acceptStateJournalMaxSeen {
		acceptStateJournalSeen = make(map[string]struct{})
	}
	acceptStateJournalSeen[key] = struct{}{}
	acceptStateJournalMutex.Unlock()

	var missing []string
	for _, input := range transaction.Inputs {
		if input.UTXOEntry == nil {
			if len(missing) < maxRejectionInputsListed {
				missing = append(missing, fmt.Sprintf("%s:%d", input.PreviousOutpoint.TransactionID,
					input.PreviousOutpoint.Index))
			}
		}
	}
	verdict := "REJECTED"
	if accepted {
		verdict = "ACCEPTED"
	}
	log.Warnf("[ACCEPT-STATE] verdict=%s block=%s tx=%s mergeSetBlock=%v inputs=%d resolved=%d "+
		"inheritsOffset=%t %s missing=[%s]",
		verdict, blockHash, transactionID, mergeSetBlockHash, len(transaction.Inputs), resolvedInputs,
		inheritsOffset, csm.explainInheritsOffset(stagingArea, blockHash), strings.Join(missing, ","))
}
