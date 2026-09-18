package model

import (
	"sort"

	"github.com/pkg/errors"
)

// TransactionsOrderedByFeeRate represents a set of MempoolTransactions ordered by their fee / mass rate
type TransactionsOrderedByFeeRate struct {
	slice []*MempoolTransaction
}

// GetByIndex returns the transaction in the given index, or nil if the index is out of bounds.
//
// tobf.slice is meant to mirror the pool's transaction set 1:1, but transactions_pool.go's own
// removeTransaction tolerates Remove failing to find an entry here ("This should never happen but
// sometimes does"), which means the two can fall out of sync in production: a transaction present
// elsewhere in the pool with no corresponding slot here. RemoveAtIndex already bounds-checks for
// exactly this reason; GetByIndex is the read-side counterpart.
func (tobf *TransactionsOrderedByFeeRate) GetByIndex(index int) *MempoolTransaction {
	if index < 0 || index >= len(tobf.slice) {
		return nil
	}
	return tobf.slice[index]
}

// Push inserts a transaction into the set, placing it in the correct place to preserve order
func (tobf *TransactionsOrderedByFeeRate) Push(transaction *MempoolTransaction) error {
	index, _, err := tobf.findTransactionIndex(transaction)
	if err != nil {
		return err
	}

	tobf.slice = append(tobf.slice[:index],
		append([]*MempoolTransaction{transaction}, tobf.slice[index:]...)...)

	return nil
}

// ErrTransactionNotFound  is returned bt tobf.TransactionsOrderedByFeeRate
var ErrTransactionNotFound = errors.New("Couldn't find transaction in mp.orderedTransactionsByFeeRate")

// Remove removes the given transaction from the set.
// Returns an error if transaction does not exist in the set, or if the given transaction does not have mass
// and fee filled in.
func (tobf *TransactionsOrderedByFeeRate) Remove(transaction *MempoolTransaction) error {
	index, wasFound, err := tobf.findTransactionIndex(transaction)
	if err != nil {
		return err
	}

	if !wasFound {
		return errors.Wrapf(ErrTransactionNotFound,
			"Couldn't find %s in mp.orderedTransactionsByFeeRate", transaction.TransactionID())
	}

	return tobf.RemoveAtIndex(index)
}

// RemoveAtIndex removes the transaction at the given index.
// Returns an error in case of out-of-bounds index.
func (tobf *TransactionsOrderedByFeeRate) RemoveAtIndex(index int) error {
	if index < 0 || index > len(tobf.slice)-1 {
		return errors.Errorf("Index %d is out of bound of this TransactionsOrderedByFeeRate", index)
	}
	tobf.slice = append(tobf.slice[:index], tobf.slice[index+1:]...)
	return nil
}

// findTransactionIndex finds the given transaction inside the list of transactions ordered by fee rate.
// If the transaction was not found, will return wasFound=false and index=the index at which transaction can be inserted
// while preserving the order.
func (tobf *TransactionsOrderedByFeeRate) findTransactionIndex(transaction *MempoolTransaction) (index int, wasFound bool, err error) {
	if transaction.Transaction().LoadFee() == 0 || transaction.Transaction().LoadMass() == 0 {
		return 0, false, errors.Errorf("findTransactionIndex expects a transaction with " +
			"populated fee and mass")
	}
	txID := transaction.TransactionID()
	txFeeRate := float64(transaction.Transaction().LoadFee()) / float64(transaction.Transaction().LoadMass())

	index = sort.Search(len(tobf.slice), func(i int) bool {
		iElement := tobf.slice[i]
		elementFeeRate := float64(iElement.Transaction().LoadFee()) / float64(iElement.Transaction().LoadMass())
		if elementFeeRate > txFeeRate {
			return true
		}

		if elementFeeRate == txFeeRate && txID.LessOrEqual(iElement.TransactionID()) {
			return true
		}

		return false
	})

	wasFound = index != len(tobf.slice) && // sort.Search returns len(tobf.slice) if nothing was found
		tobf.slice[index].TransactionID().Equal(transaction.TransactionID())

	return index, wasFound, nil
}
