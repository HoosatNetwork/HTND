package model

import "github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"

// CoinbaseManager exposes methods for handling blocks'
// coinbase transactions
type CoinbaseManager interface {
	ExpectedCoinbaseTransaction(stagingArea *StagingArea, blockHash *externalapi.DomainHash,
		coinbaseData *externalapi.DomainCoinbaseData) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error)
	ExpectedCoinbaseTransactionWithAcceptanceData(stagingArea *StagingArea, blockHash *externalapi.DomainHash,
		coinbaseData *externalapi.DomainCoinbaseData, acceptanceData externalapi.AcceptanceData) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error)
	CalcBlockSubsidy(stagingArea *StagingArea, blockHash *externalapi.DomainHash, blockVersion uint16) (uint64, error)
	ExtractCoinbaseDataBlueScoreAndSubsidyForVersion(coinbaseTx *externalapi.DomainTransaction, blockVersion uint16) (blueScore uint64, coinbaseData *externalapi.DomainCoinbaseData, subsidy uint64, err error)
}
