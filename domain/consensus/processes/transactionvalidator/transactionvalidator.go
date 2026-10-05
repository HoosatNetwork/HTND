package transactionvalidator

import (
	"sync"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/txscript"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/v2/util/txmass"
)

const sigCacheSize = 10_000

// mldsa44SigCacheSize and mldsa44PublicKeyCacheSize size the ML-DSA-44 cache. A verified signature
// costs under 100 bytes, so the signature side is sized for many blocks' worth of spends; a parsed
// public key costs about 20 KB, so 2,000 of them is about 40 MB.
const (
	mldsa44SigCacheSize       = 100_000
	mldsa44PublicKeyCacheSize = 2_000
)

// transactionValidator exposes a set of validation classes, after which
// it's possible to determine whether either a transaction is valid
type transactionValidator struct {
	blockCoinbaseMaturity                   uint64
	databaseContext                         model.DBReader
	pastMedianTimeManager                   model.PastMedianTimeManager
	ghostdagDataStore                       model.GHOSTDAGDataStore
	daaBlocksStore                          model.DAABlocksStore
	enableNonNativeSubnetworks              bool
	maxCoinbasePayloadLength                uint64
	MergeSetSizeLimit                       uint64
	coinbasePayloadScriptPublicKeyMaxLength uint8
	sigCache                                *txscript.SigCache
	sigCacheECDSA                           *txscript.SigCacheECDSA
	mldsa44Cache                            *txscript.MLDSA44Cache
	txMassCalculator                        *txmass.Calculator
	enginePool                              *sync.Pool

	// dagParams supplies POWScores and the block versions of script-level forks, used to decide
	// which script flags apply at a given DAA score without going through the process-global
	// block version.
	dagParams *dagconfig.Params
}

// New instantiates a new TransactionValidator
func New(blockCoinbaseMaturity uint64,
	enableNonNativeSubnetworks bool,
	maxCoinbasePayloadLength uint64,
	mergeSetSizeLimit uint64,
	coinbasePayloadScriptPublicKeyMaxLength uint8,
	databaseContext model.DBReader,
	pastMedianTimeManager model.PastMedianTimeManager,
	ghostdagDataStore model.GHOSTDAGDataStore,
	daaBlocksStore model.DAABlocksStore,
	txMassCalculator *txmass.Calculator,
	dagParams *dagconfig.Params,
) model.TransactionValidator {
	return &transactionValidator{
		blockCoinbaseMaturity:                   blockCoinbaseMaturity,
		enableNonNativeSubnetworks:              enableNonNativeSubnetworks,
		maxCoinbasePayloadLength:                maxCoinbasePayloadLength,
		MergeSetSizeLimit:                       mergeSetSizeLimit,
		coinbasePayloadScriptPublicKeyMaxLength: coinbasePayloadScriptPublicKeyMaxLength,
		databaseContext:                         databaseContext,
		pastMedianTimeManager:                   pastMedianTimeManager,
		ghostdagDataStore:                       ghostdagDataStore,
		daaBlocksStore:                          daaBlocksStore,
		sigCache:                                txscript.NewSigCache(sigCacheSize),
		sigCacheECDSA:                           txscript.NewSigCacheECDSA(sigCacheSize),
		mldsa44Cache:                            txscript.NewMLDSA44Cache(mldsa44SigCacheSize, mldsa44PublicKeyCacheSize),
		txMassCalculator:                        txMassCalculator,
		dagParams:                               dagParams,
		enginePool: &sync.Pool{
			New: func() any {
				return &txscript.Engine{}
			},
		},
	}
}
