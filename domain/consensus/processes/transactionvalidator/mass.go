package transactionvalidator

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// PopulateMass calculates and populates the mass of the given transaction
func (v *transactionValidator) PopulateMass(transaction *externalapi.DomainTransaction) {
	if transaction.LoadMass() != 0 {
		return
	}
	// Concurrent callers may both compute the mass; they get the same value, so
	// the last store winning is harmless.
	transaction.StoreMass(v.txMassCalculator.CalculateTransactionMass(transaction))
}
