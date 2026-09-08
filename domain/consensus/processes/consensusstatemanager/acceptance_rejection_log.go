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

// noteAcceptanceRejection reports, at the default log level, that this node refused a transaction
// during block acceptance because it does not hold a coin the transaction spends.
//
// This is the single most important thing a node can say about its own health, and until now it
// said nothing. Acceptance decides what goes into the acceptance data, which decides the accepted-ID
// merkle root and the UTXO commitment - so two nodes that disagree here produce different
// commitments for the same block and permanently disagree about whether a transaction is spendable.
// That was observed live: two synced nodes, the same accepting block, opposite verdicts on 30 of 41
// transactions, with nothing in either log to say why.
//
// A double spend is deliberately not reported. On a DAG the same transaction sits in several blocks
// and only the first merge accepts it, so rejecting the later copies is correct and constant. Only
// a coin this node cannot find is a statement about this node.
func (csm *consensusStateManager) noteAcceptanceRejection(blockHash *externalapi.DomainHash,
	transactionID *externalapi.DomainTransactionID, err error,
) {
	var missingTxOut ruleerrors.ErrMissingTxOut
	if !errors.As(err, &missingTxOut) || missingTxOut.HasDoubleSpend() {
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
	}

	if time.Since(csm.missingInputLastReport) < missingInputReportInterval {
		return
	}
	csm.missingInputLastReport = time.Now()

	log.Warnf("Rejected %d transaction(s) during block acceptance because this node does not hold "+
		"coins they spend. This node's acceptance data therefore differs from that of a node which "+
		"does hold them, and so will its UTXO commitment. Example: transaction %s in block %s spends "+
		"%s:%d, which is one of %d inputs this node could not find",
		csm.missingInputRejections, csm.missingInputExampleTx, csm.missingInputExampleBlock,
		csm.missingInputExample, csm.missingInputExampleIndex, csm.missingInputExampleCount)

	csm.missingInputRejections = 0
	csm.missingInputExample = ""
}
