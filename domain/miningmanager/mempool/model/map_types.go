package model

import (
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
)

// IDToTransactionMap maps transactionID to a MempoolTransaction
type IDToTransactionMap map[externalapi.DomainTransactionID]*MempoolTransaction

// IDToTransactionsSliceMap maps transactionID to a slice MempoolTransaction
type IDToTransactionsSliceMap map[externalapi.DomainTransactionID][]*MempoolTransaction

// OutpointToUTXOEntryMap maps an outpoint to a UTXOEntry
type OutpointToUTXOEntryMap map[externalapi.DomainOutpoint]externalapi.UTXOEntry

// OutpointToTransactionMap maps an outpoint to a MempoolTransaction
type OutpointToTransactionMap map[externalapi.DomainOutpoint]*MempoolTransaction

// ScriptPublicKeyStringToDomainTransactions maps a script public key to every DomainTransaction that
// spends from it or pays to it
type ScriptPublicKeyStringToDomainTransactions map[string][]*externalapi.DomainTransaction

// Add lists transaction under key once. A transaction spending several coins of one address, or paying it
// several outputs, is added for each of them in a row, so comparing with the last one listed is enough.
func (m ScriptPublicKeyStringToDomainTransactions) Add(key string, transaction *externalapi.DomainTransaction) {
	transactions := m[key]
	if len(transactions) > 0 && transactions[len(transactions)-1] == transaction {
		return
	}
	m[key] = append(transactions, transaction)
}
