package consensusstatemanager

import (
	"fmt"
	"strings"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/pkg/errors"
)

// maxRejectionInputsListed bounds how many inputs one debug line spells out. Compound transactions
// carry hundreds of inputs, and a line that long is unreadable and slow to write on every merge.
// Unresolved inputs are listed first, so the ones that explain the verdict are never the ones cut.
const maxRejectionInputsListed = 32

// transactionVerdictContext is where in the DAG a merge-set transaction was judged. The merging
// block is the one whose past UTXO is being built; the merge-set block is the one that carries the
// transaction. They differ for every transaction outside the selected parent, and a rejection is
// only reproducible knowing both.
type transactionVerdictContext struct {
	blockHash         *externalapi.DomainHash
	mergeSetBlockHash *externalapi.DomainHash
	isSelectedParent  bool
	blockDAAScore     uint64
}

// logTransactionVerdict writes, at debug level, everything this node knew about a merge-set
// transaction when it did not accept it as-is: where it was judged, why, and the state of each input
// as this view resolved it. It is diagnostic only and never affects the verdict, and it is skipped
// entirely unless the subsystem logs at debug, because building it walks every input.
//
// The existing lines are not enough to reproduce a rejection: the trace line carries only the
// error, the periodic missing-input warning keeps one example, and the survey records the reason
// only when it is switched on. Two nodes that disagree about a block's acceptance data need to
// compare the per-input view each of them had, which is what this line prints.
func logTransactionVerdict(verdict string, ctx *transactionVerdictContext,
	transaction *externalapi.DomainTransaction, transactionID string, err error, extra string,
) {
	if log.Level() > logger.LevelDebug {
		return
	}
	log.Debug(describeTransactionVerdict(verdict, ctx, transaction, transactionID, err, extra))
}

func describeTransactionVerdict(verdict string, ctx *transactionVerdictContext,
	transaction *externalapi.DomainTransaction, transactionID string, err error, extra string,
) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "[TX-VERDICT] %s: transaction %s", verdict, transactionID)
	if ctx != nil {
		fmt.Fprintf(&builder, " in merge-set block %v merged by block %v (selected parent: %t, merging DAA score: %d)",
			ctx.mergeSetBlockHash, ctx.blockHash, ctx.isSelectedParent, ctx.blockDAAScore)
	}
	if extra != "" {
		fmt.Fprintf(&builder, "; %s", extra)
	}
	if err != nil {
		fmt.Fprintf(&builder, "; error: %s", err)
	}
	if transaction == nil {
		return builder.String()
	}

	var totalOut uint64
	for _, output := range transaction.Outputs {
		if output != nil {
			totalOut += output.Value
		}
	}
	fmt.Fprintf(&builder, "; version %d, subnetwork %s, lock time %d, gas %d, payload %d bytes, mass %d, "+
		"%d outputs totalling %d sompi",
		transaction.Version, transaction.SubnetworkID, transaction.LockTime, transaction.Gas,
		len(transaction.Payload), transaction.LoadMass(), len(transaction.Outputs), totalOut)

	// Classify each unresolved input by the error's own lists: a spent input is a double spend in
	// this view, anything else unresolved is simply absent from it.
	spent := make(map[externalapi.DomainOutpoint]struct{})
	var missingTxOut ruleerrors.ErrMissingTxOut
	if errors.As(err, &missingTxOut) {
		for _, outpoint := range missingTxOut.SpentOutpoints {
			if outpoint != nil {
				spent[*outpoint] = struct{}{}
			}
		}
	}

	resolved := 0
	var totalIn uint64
	unresolvedFirst := make([]int, 0, len(transaction.Inputs))
	var resolvedIndexes []int
	for i, input := range transaction.Inputs {
		if input != nil && input.UTXOEntry != nil {
			resolved++
			totalIn += input.UTXOEntry.Amount()
			resolvedIndexes = append(resolvedIndexes, i)
			continue
		}
		unresolvedFirst = append(unresolvedFirst, i)
	}
	unresolvedFirst = append(unresolvedFirst, resolvedIndexes...)

	fmt.Fprintf(&builder, "; %d inputs, %d resolved totalling %d sompi, %d unresolved",
		len(transaction.Inputs), resolved, totalIn, len(transaction.Inputs)-resolved)
	if resolved == len(transaction.Inputs) && totalIn >= totalOut {
		fmt.Fprintf(&builder, " (implied fee %d)", totalIn-totalOut)
	}

	for listed, i := range unresolvedFirst {
		if listed == maxRejectionInputsListed {
			fmt.Fprintf(&builder, "; ... %d more inputs not listed", len(unresolvedFirst)-listed)
			break
		}
		input := transaction.Inputs[i]
		if input == nil {
			fmt.Fprintf(&builder, "; input %d: <nil>", i)
			continue
		}
		fmt.Fprintf(&builder, "; input %d %s seq %d sigops %d: ", i, input.PreviousOutpoint,
			input.Sequence, input.SigOpCount)
		entry := input.UTXOEntry
		if entry == nil {
			if _, ok := spent[input.PreviousOutpoint]; ok {
				builder.WriteString("SPENT in this view")
			} else {
				builder.WriteString("ABSENT from this view")
			}
			continue
		}
		fmt.Fprintf(&builder, "amount %d, DAA score %d, coinbase %t", entry.Amount(), entry.BlockDAAScore(),
			entry.IsCoinbase())
	}
	return builder.String()
}
