package model

import (
	"time"

	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
)

// OrphanTransaction represents a transaction in the OrphanPool
type OrphanTransaction struct {
	transaction     *externalapi.DomainTransaction
	isHighPriority  bool
	addedAtDAAScore uint64
	addedAtTime     time.Time
}

// NewOrphanTransaction constructs a new OrphanTransaction
func NewOrphanTransaction(
	transaction *externalapi.DomainTransaction,
	isHighPriority bool,
	addedAtDAAScore uint64,
) *OrphanTransaction {
	return &OrphanTransaction{
		transaction:     transaction,
		isHighPriority:  isHighPriority,
		addedAtDAAScore: addedAtDAAScore,
		addedAtTime:     time.Now(),
	}
}

// TransactionID returns the ID of this OrphanTransaction
func (ot *OrphanTransaction) TransactionID() *externalapi.DomainTransactionID {
	return consensushashing.TransactionID(ot.transaction)
}

// Transaction return the DomainTransaction associated with this OrphanTransaction:
func (ot *OrphanTransaction) Transaction() *externalapi.DomainTransaction {
	return ot.transaction
}

// IsHighPriority returns whether this OrphanTransaction is a high-priority one
func (ot *OrphanTransaction) IsHighPriority() bool {
	return ot.isHighPriority
}

// AddedAtDAAScore returns the virtual DAA score at which this OrphanTransaction was added to the mempool
func (ot *OrphanTransaction) AddedAtDAAScore() uint64 {
	return ot.addedAtDAAScore
}

// AddedAtTime returns the wall-clock time when this orphan was first seen
func (ot *OrphanTransaction) AddedAtTime() time.Time {
	return ot.addedAtTime
}
