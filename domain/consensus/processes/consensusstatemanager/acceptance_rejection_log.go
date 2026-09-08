package consensusstatemanager

import (
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/pkg/errors"
)

// missingInputReportInterval is how often the missing-input summary is emitted. A node whose UTXO
// set has holes hits this on a large fraction of blocks, so one line per rejection would be
// unreadable and one line per process would hide the scale. A periodic count with an example gives
// both.
const missingInputReportInterval = 30 * time.Second

// missingInputReportFormat is the report wording, named so a test can assert it does not claim
// more than the check establishes.
const missingInputReportFormat = "Rejected %d transaction(s) during block acceptance for coins this node could not " +
	"find, having found their other inputs - which is the shape of a gap in this node's UTXO " +
	"set rather than of a transaction already accepted elsewhere. A node that does hold those " +
	"coins will produce different acceptance data, and so a different UTXO commitment, for the " +
	"same block. Example: transaction %s in block %s spends %s:%d; %d of its %d inputs were not " +
	"found"

// noteAcceptanceRejection reports, at the default log level, that this node refused a transaction
// during block acceptance because it could not find coins the transaction spends AND that refusal
// does not look like ordinary duplicate handling.
//
// The distinction is the whole value of the line, and getting it wrong makes the line worse than
// useless. On a DAG the same transaction is carried by several blocks and only the first merge
// accepts it; every later copy is refused because the coins are already consumed. That is correct,
// constant, and says nothing about this node's health. It is also indistinguishable at this point
// from a genuine gap, because populateTransactionWithUTXOEntriesFromVirtualOrDiff can only tell a
// double spend from a missing coin when the spend happened inside the accumulated diff it was given
// - a spend folded into virtual by an earlier block reads simply as "not found".
//
// The discriminator used here is how MANY inputs went missing. A transaction that was already
// accepted has had every one of its inputs consumed, so all of them come back missing. A node with
// holes in its UTXO set is missing particular coins, so a transaction spending eighty-eight of them
// typically finds most and fails on a few. Reporting only the partial case keeps the line quiet on
// healthy nodes and loud on damaged ones.
//
// This is a heuristic, not a proof: a gap that happens to cover every input of some transaction
// will be missed, and a duplicate whose inputs were partly re-created would be reported. It is
// calibrated to be quiet when wrong in the first direction rather than noisy when wrong in the
// second, because the first failure loses one report and the second discredits every report.
//
// Verified against live data: three transactions this check originally reported were all whole-
// transaction cases, and two of them were confirmed by scanning acceptance data to have been
// accepted earlier - the rejections were later copies of transactions this node had already taken.
func (csm *consensusStateManager) noteAcceptanceRejection(blockHash *externalapi.DomainHash,
	transactionID *externalapi.DomainTransactionID, inputCount int, err error,
) {
	var missingTxOut ruleerrors.ErrMissingTxOut
	if !errors.As(err, &missingTxOut) || missingTxOut.HasDoubleSpend() {
		return
	}
	// Every input gone: this transaction was almost certainly accepted already and this is a later
	// copy of it. Not a statement about this node.
	if inputCount > 0 && len(missingTxOut.MissingOutpoints) >= inputCount {
		return
	}
	// maybeAcceptTransaction tolerates a nil transaction id rather than failing on it, so this must
	// too - a diagnostic must never be the thing that brings acceptance down.
	if blockHash == nil || transactionID == nil {
		return
	}

	csm.missingInputMutex.Lock()
	defer csm.missingInputMutex.Unlock()

	csm.missingInputRejections++
	if csm.missingInputExample == "" && len(missingTxOut.MissingOutpoints) > 0 {
		outpoint := missingTxOut.MissingOutpoints[0]
		csm.missingInputExample = outpoint.TransactionID.String()
		csm.missingInputExampleIndex = outpoint.Index
		csm.missingInputExampleTx = transactionID.String()
		csm.missingInputExampleBlock = blockHash.String()
		csm.missingInputExampleCount = len(missingTxOut.MissingOutpoints)
		csm.missingInputExampleInputs = inputCount
	}

	if time.Since(csm.missingInputLastReport) < missingInputReportInterval {
		return
	}
	csm.missingInputLastReport = time.Now()

	log.Warnf(missingInputReportFormat,
		csm.missingInputRejections, csm.missingInputExampleTx, csm.missingInputExampleBlock,
		csm.missingInputExample, csm.missingInputExampleIndex, csm.missingInputExampleCount,
		csm.missingInputExampleInputs)

	csm.missingInputRejections = 0
	csm.missingInputExample = ""
}

// acceptanceRejectionMessageForTest exposes the report wording so a test can assert it does not
// overclaim. Kept beside the log call so the two cannot drift.
func acceptanceRejectionMessageForTest() string {
	return missingInputReportFormat
}
