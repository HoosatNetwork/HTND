package model

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// TransactionValidator exposes a set of validation classes, after which
// it's possible to determine whether a transaction is valid
type TransactionValidator interface {
	ValidateTransactionInIsolation(transaction *externalapi.DomainTransaction, povDAAScore uint64) error
	ValidateTransactionInContextIgnoringUTXO(stagingArea *StagingArea, tx *externalapi.DomainTransaction,
		povBlockHash *externalapi.DomainHash, povBlockPastMedianTime int64, povDAAScore uint64) error
	ValidateTransactionInContextAndPopulateFee(stagingArea *StagingArea,
		tx *externalapi.DomainTransaction, povBlockHash *externalapi.DomainHash, povDAAScore uint64) error
	// ValidateTransactionWithMissingInputsAndPopulateFee runs every in-context check that the inputs
	// present in tx can decide, and requires outputs <= those inputs. Missing inputs count for nothing.
	ValidateTransactionWithMissingInputsAndPopulateFee(stagingArea *StagingArea,
		tx *externalapi.DomainTransaction, povBlockHash *externalapi.DomainHash, povDAAScore uint64) error
	PopulateMass(transaction *externalapi.DomainTransaction)
}
