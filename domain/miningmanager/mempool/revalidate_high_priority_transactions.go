package mempool

import (
	"fmt"
	"strings"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool/model"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

func (mp *mempool) revalidateHighPriorityTransactions() ([]*externalapi.DomainTransaction, error) {
	type txNode struct {
		children          map[externalapi.DomainTransactionID]struct{}
		nonVisitedParents int
		tx                *model.MempoolTransaction
		visited           bool
	}

	onEnd := logger.LogAndMeasureExecutionTime(log, "revalidateHighPriorityTransactions")
	defer onEnd()

	// We revalidate transactions in topological order in case there are dependencies between them

	// Naturally transactions point to their dependencies, but since we want to start processing the dependencies
	// first, we build the opposite DAG. We initially fill `queue` with transactions with no dependencies.
	txDAG := make(map[externalapi.DomainTransactionID]*txNode)

	maybeAddNode := func(txID externalapi.DomainTransactionID) *txNode {
		if node, ok := txDAG[txID]; ok {
			return node
		}

		node := &txNode{
			children:          make(map[externalapi.DomainTransactionID]struct{}),
			nonVisitedParents: 0,
			tx:                mp.transactionsPool.highPriorityTransactions[txID],
		}
		txDAG[txID] = node
		return node
	}

	queue := make([]*txNode, 0, len(mp.transactionsPool.highPriorityTransactions))
	for id, transaction := range mp.transactionsPool.highPriorityTransactions {
		node := maybeAddNode(id)

		parents := make(map[externalapi.DomainTransactionID]struct{})
		for _, input := range transaction.Transaction().Inputs {
			if _, ok := mp.transactionsPool.highPriorityTransactions[input.PreviousOutpoint.TransactionID]; !ok {
				continue
			}

			parents[input.PreviousOutpoint.TransactionID] = struct{}{} // To avoid duplicate parents, we first add it to a set and then count it
			maybeAddNode(input.PreviousOutpoint.TransactionID).children[id] = struct{}{}
		}
		node.nonVisitedParents = len(parents)

		if node.nonVisitedParents == 0 {
			queue = append(queue, node)
		}
	}

	validTransactions := []*externalapi.DomainTransaction{}

	// Now we iterate the DAG in topological order using BFS
	for len(queue) > 0 {
		var node *txNode
		node, queue = queue[0], queue[1:]

		if node.visited {
			continue
		}
		node.visited = true

		transaction := node.tx
		isValid, err := mp.revalidateTransaction(transaction)
		if err != nil {
			return nil, err
		}

		for child := range node.children {
			childNode := txDAG[child]
			childNode.nonVisitedParents--
			if childNode.nonVisitedParents == 0 {
				queue = append(queue, txDAG[child])
			}
		}

		if isValid {
			validTransactions = append(validTransactions, transaction.Transaction().Clone())
		}
	}

	return validTransactions, nil
}

func (mp *mempool) revalidateTransaction(transaction *model.MempoolTransaction) (isValid bool, err error) {
	clearInputs(transaction)

	_, missingParents, err := mp.fillInputsAndGetMissingParents(transaction.Transaction())
	if err != nil {
		return false, err
	}
	if len(missingParents) > 0 {
		// Warn, not debug. These are high-priority transactions, which on this node means locally
		// submitted ones - somebody's wallet sent this and got an id back. Dropping it here makes it
		// vanish from every RPC answer, including on the node that accepted it seconds earlier, and
		// the submitter is never told. Days were spent treating that disappearance as a propagation
		// failure, because at the default log level there was nothing to read.
		//
		// The missing outpoints are named because they are the whole diagnosis: a transaction whose
		// inputs cannot be found is not a bad transaction, it is a node that does not hold coins the
		// transaction's author could see. Volume is not a concern - these are local submissions.
		log.Warnf("Removing locally submitted transaction %s from the mempool: %d of its inputs "+
			"cannot be found in this node's UTXO set (%s). The transaction was accepted earlier and "+
			"will now report as not-found",
			transaction.TransactionID(), len(missingParents), formatOutpoints(missingParents))
		err := mp.removeTransaction(transaction.TransactionID(), false)
		if err != nil {
			return false, err
		}
		return false, nil
	}

	return true, nil
}

func clearInputs(transaction *model.MempoolTransaction) {
	for _, input := range transaction.Transaction().Inputs {
		input.UTXOEntry = nil
	}
}

// formatOutpoints renders at most a handful of outpoints for a log line, so a transaction with a
// hundred inputs - a compounding transaction, say - names enough of them to be traced without
// filling the log with one line's worth of hashes.
func formatOutpoints(outpoints []*externalapi.DomainOutpoint) string {
	const maxListed = 4

	listed := outpoints
	suffix := ""
	if len(listed) > maxListed {
		listed = listed[:maxListed]
		suffix = fmt.Sprintf(" and %d more", len(outpoints)-maxListed)
	}

	parts := make([]string, 0, len(listed))
	for _, outpoint := range listed {
		parts = append(parts, fmt.Sprintf("%s:%d", outpoint.TransactionID, outpoint.Index))
	}
	return strings.Join(parts, ", ") + suffix
}
