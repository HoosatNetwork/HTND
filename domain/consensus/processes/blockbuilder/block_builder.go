package blockbuilder

import (
	"math/big"
	"sort"

	"github.com/HoosatNetwork/HTND/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/merkle"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/mstime"
)

type blockBuilder struct {
	databaseContext model.DBManager
	genesisHash     *externalapi.DomainHash
	powScores       []uint64

	difficultyManager     model.DifficultyManager
	pastMedianTimeManager model.PastMedianTimeManager
	coinbaseManager       model.CoinbaseManager
	consensusStateManager model.ConsensusStateManager
	ghostdagManager       model.GHOSTDAGManager
	transactionValidator  model.TransactionValidator
	finalityManager       model.FinalityManager
	pruningManager        model.PruningManager
	blockParentBuilder    model.BlockParentBuilder

	acceptanceDataStore model.AcceptanceDataStore
	blockRelationStore  model.BlockRelationStore
	multisetStore       model.MultisetStore
	ghostdagDataStore   model.GHOSTDAGDataStore
	daaBlocksStore      model.DAABlocksStore
}

// New creates a new instance of a BlockBuilder
func New(
	databaseContext model.DBManager,
	genesisHash *externalapi.DomainHash,
	powScores []uint64,

	difficultyManager model.DifficultyManager,
	pastMedianTimeManager model.PastMedianTimeManager,
	coinbaseManager model.CoinbaseManager,
	consensusStateManager model.ConsensusStateManager,
	ghostdagManager model.GHOSTDAGManager,
	transactionValidator model.TransactionValidator,
	finalityManager model.FinalityManager,
	blockParentBuilder model.BlockParentBuilder,
	pruningManager model.PruningManager,

	acceptanceDataStore model.AcceptanceDataStore,
	blockRelationStore model.BlockRelationStore,
	multisetStore model.MultisetStore,
	ghostdagDataStore model.GHOSTDAGDataStore,
	daaBlocksStore model.DAABlocksStore,
) model.BlockBuilder {
	return &blockBuilder{
		databaseContext: databaseContext,
		genesisHash:     genesisHash,
		powScores:       powScores,

		difficultyManager:     difficultyManager,
		pastMedianTimeManager: pastMedianTimeManager,
		coinbaseManager:       coinbaseManager,
		consensusStateManager: consensusStateManager,
		ghostdagManager:       ghostdagManager,
		transactionValidator:  transactionValidator,
		finalityManager:       finalityManager,
		blockParentBuilder:    blockParentBuilder,
		pruningManager:        pruningManager,

		acceptanceDataStore: acceptanceDataStore,
		blockRelationStore:  blockRelationStore,
		multisetStore:       multisetStore,
		ghostdagDataStore:   ghostdagDataStore,
		daaBlocksStore:      daaBlocksStore,
	}
}

// BuildBlock builds a block over the current state, with the given
// coinbaseData and the given transactions
func (bb *blockBuilder) BuildBlock(coinbaseData *externalapi.DomainCoinbaseData,
	transactions []*externalapi.DomainTransaction,
) (block *externalapi.DomainBlock, coinbaseHasRedReward bool, err error) {
	onEnd := logger.LogAndMeasureExecutionTime(log, "BuildBlock")
	defer onEnd()

	stagingArea := model.NewStagingArea()

	return bb.buildBlock(stagingArea, coinbaseData, transactions)
}

func (bb *blockBuilder) buildBlock(stagingArea *model.StagingArea, coinbaseData *externalapi.DomainCoinbaseData,
	transactions []*externalapi.DomainTransaction,
) (block *externalapi.DomainBlock, coinbaseHasRedReward bool, err error) {
	err = bb.validateTransactions(stagingArea, transactions)
	if err != nil {
		return nil, false, err
	}

	newBlockPruningPoint, err := bb.newBlockPruningPoint(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, false, err
	}
	newBlockDAAScore, err := bb.newBlockDAAScore(stagingArea)
	if err != nil {
		return nil, false, err
	}
	constants.SetBlockVersion(bb.blockVersionForDAAScore(newBlockDAAScore))

	// The timestamp must be picked before the coinbase is built (rather than inside buildHeader, as
	// before) so it can be folded into coinbase entropy from CoinbaseTimestampEntropyActivationVersion
	// onward (see coinbasemanager.coinbaseEntropy), and the exact same value must then be reused
	// verbatim for the header itself rather than recomputed - recomputing would risk a different
	// wall-clock value and desync the coinbase's entropy from what a validator recomputes from the
	// stored header.
	newBlockTimeInMilliseconds, err := bb.newBlockTime(stagingArea)
	if err != nil {
		return nil, false, err
	}

	coinbase, coinbaseHasRedReward, err := bb.newBlockCoinbaseTransaction(stagingArea, coinbaseData, newBlockTimeInMilliseconds)
	if err != nil {
		return nil, false, err
	}
	transactionsWithCoinbase := append([]*externalapi.DomainTransaction{coinbase}, transactions...)

	header, err := bb.buildHeader(stagingArea, transactionsWithCoinbase, newBlockPruningPoint, newBlockTimeInMilliseconds)
	if err != nil {
		return nil, false, err
	}

	return &externalapi.DomainBlock{
		Header:       header,
		Transactions: transactionsWithCoinbase,
	}, coinbaseHasRedReward, nil
}

func (bb *blockBuilder) validateTransactions(stagingArea *model.StagingArea,
	transactions []*externalapi.DomainTransaction,
) error {
	if len(transactions) == 0 {
		return nil
	}

	invalidTransactions := make([]ruleerrors.InvalidTransaction, 0, 20)
	for i := range transactions {
		err := bb.validateTransaction(stagingArea, transactions[i])
		if err != nil {
			ruleError := ruleerrors.RuleError{}
			if !errors.As(err, &ruleError) {
				return err
			}
			invalidTransactions = append(invalidTransactions, ruleerrors.InvalidTransaction{Transaction: transactions[i], Error: &ruleError})
		}
	}

	if len(invalidTransactions) > 0 {
		return ruleerrors.NewErrInvalidTransactionsInNewBlock(invalidTransactions)
	}

	return nil
}

func (bb *blockBuilder) validateTransaction(
	stagingArea *model.StagingArea, transaction *externalapi.DomainTransaction,
) error {
	originalEntries := make([]externalapi.UTXOEntry, len(transaction.Inputs))
	for i := 0; i < len(transaction.Inputs); i++ {
		originalEntries[i] = transaction.Inputs[i].UTXOEntry
		transaction.Inputs[i].UTXOEntry = nil
	}

	defer func() {
		for i := 0; i < len(transaction.Inputs); i++ {
			transaction.Inputs[i].UTXOEntry = originalEntries[i]
		}
	}()

	err := bb.consensusStateManager.PopulateTransactionWithUTXOEntries(stagingArea, transaction)
	if err != nil {
		return err
	}

	virtualPastMedianTime, err := bb.pastMedianTimeManager.PastMedianTime(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}

	// Fetch the virtual DAA score to pass as POV DAA score
	virtualDAAScore, err := bb.daaBlocksStore.DAAScore(bb.databaseContext, stagingArea, model.VirtualBlockHash)
	if err != nil {
		return err
	}

	err = bb.transactionValidator.ValidateTransactionInContextIgnoringUTXO(stagingArea, transaction, model.VirtualBlockHash, virtualPastMedianTime, virtualDAAScore)
	if err != nil {
		return err
	}

	return bb.transactionValidator.ValidateTransactionInContextAndPopulateFee(stagingArea, transaction, model.VirtualBlockHash, virtualDAAScore)
}

func (bb *blockBuilder) newBlockCoinbaseTransaction(stagingArea *model.StagingArea,
	coinbaseData *externalapi.DomainCoinbaseData, candidateTimestamp int64,
) (expectedTransaction *externalapi.DomainTransaction, hasRedReward bool, err error) {
	return bb.coinbaseManager.ExpectedCoinbaseTransaction(stagingArea, model.VirtualBlockHash, coinbaseData, candidateTimestamp)
}

func (bb *blockBuilder) buildHeader(stagingArea *model.StagingArea, transactions []*externalapi.DomainTransaction,
	newBlockPruningPoint *externalapi.DomainHash, timeInMilliseconds int64,
) (externalapi.BlockHeader, error) {
	daaScore, err := bb.newBlockDAAScore(stagingArea)
	if err != nil {
		return nil, err
	}

	parents, err := bb.newBlockParents(stagingArea, daaScore)
	if err != nil {
		return nil, err
	}

	bits, err := bb.newBlockDifficulty(stagingArea)
	if err != nil {
		return nil, err
	}
	hashMerkleRoot := bb.newBlockHashMerkleRoot(transactions)
	acceptedIDMerkleRoot, err := bb.newBlockAcceptedIDMerkleRoot(stagingArea)
	if err != nil {
		return nil, err
	}
	utxoCommitment, err := bb.newBlockUTXOCommitment(stagingArea)
	if err != nil {
		return nil, err
	}
	blueWork, err := bb.newBlockBlueWork(stagingArea)
	if err != nil {
		return nil, err
	}
	blueScore, err := bb.newBlockBlueScore(stagingArea)
	if err != nil {
		return nil, err
	}

	blockVersion := bb.blockVersionForDAAScore(daaScore)
	constants.SetBlockVersion(blockVersion)

	return blockheader.NewImmutableBlockHeader(
		blockVersion,
		parents,
		hashMerkleRoot,
		acceptedIDMerkleRoot,
		utxoCommitment,
		timeInMilliseconds,
		bits,
		0,
		daaScore,
		blueScore,
		blueWork,
		newBlockPruningPoint,
	), nil
}

func (bb *blockBuilder) blockVersionForDAAScore(daaScore uint64) uint16 {
	var blockVersion uint16 = 1
	for _, powScore := range bb.powScores {
		if daaScore >= powScore {
			blockVersion++
		}
	}
	return blockVersion
}

func (bb *blockBuilder) newBlockParents(stagingArea *model.StagingArea, daaScore uint64) ([]externalapi.BlockLevelParents, error) {
	virtualBlockRelations, err := bb.blockRelationStore.BlockRelation(bb.databaseContext, stagingArea, model.VirtualBlockHash)
	if err != nil {
		return nil, err
	}
	newBlockParents := false
	if bb.blockVersionForDAAScore(daaScore) >= 7 {
		newBlockParents = true
	}
	return bb.blockParentBuilder.BuildParents(stagingArea, daaScore, virtualBlockRelations.Parents, newBlockParents)
}

func (bb *blockBuilder) newBlockTime(stagingArea *model.StagingArea) (int64, error) {
	// The timestamp for the block must not be before the median timestamp
	// of the last several blocks. Thus, choose the maximum between the
	// current time and one second after the past median time. The current
	// timestamp is truncated to a millisecond boundary before comparison since a
	// block timestamp does not supported a precision greater than one
	// millisecond.
	newTimestamp := mstime.Now().UnixMilliseconds()
	minTimestamp, err := bb.minBlockTime(stagingArea, model.VirtualBlockHash)
	if err != nil {
		return 0, err
	}
	if newTimestamp < minTimestamp {
		newTimestamp = minTimestamp
	}
	return newTimestamp, nil
}

func (bb *blockBuilder) minBlockTime(stagingArea *model.StagingArea, hash *externalapi.DomainHash) (int64, error) {
	pastMedianTime, err := bb.pastMedianTimeManager.PastMedianTime(stagingArea, hash)
	if err != nil {
		return 0, err
	}

	return pastMedianTime + 1, nil
}

func (bb *blockBuilder) newBlockDifficulty(stagingArea *model.StagingArea) (uint32, error) {
	return bb.difficultyManager.RequiredDifficulty(stagingArea, model.VirtualBlockHash)
}

func (bb *blockBuilder) newBlockHashMerkleRoot(transactions []*externalapi.DomainTransaction) *externalapi.DomainHash {
	return merkle.CalculateHashMerkleRoot(transactions)
}

func (bb *blockBuilder) newBlockAcceptedIDMerkleRoot(stagingArea *model.StagingArea) (*externalapi.DomainHash, error) {
	newBlockAcceptanceData, err := bb.acceptanceDataStore.Get(bb.databaseContext, stagingArea, model.VirtualBlockHash)
	if database.IsNotFoundError(err) {
		log.Infof("newBlockAcceptedIDMerkleRoot failed to retrieve with %s\n", model.VirtualBlockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	return bb.calculateAcceptedIDMerkleRoot(newBlockAcceptanceData)
}

func (bb *blockBuilder) calculateAcceptedIDMerkleRoot(acceptanceData externalapi.AcceptanceData) (*externalapi.DomainHash, error) {
	var acceptedTransactions []*externalapi.DomainTransaction
	for i := range acceptanceData {
		for x := 0; x < len(acceptanceData[i].TransactionAcceptanceData); x++ {
			if !acceptanceData[i].TransactionAcceptanceData[x].IsAccepted {
				continue
			}
			acceptedTransactions = append(acceptedTransactions, acceptanceData[i].TransactionAcceptanceData[x].Transaction)
		}
	}
	// In block version 4 and below, the accepted transactions are sorted by their IDs, in Block Version 5 and above, the order is not important
	if constants.GetBlockVersion() < 5 {
		sort.Slice(acceptedTransactions, func(i, j int) bool {
			acceptedTransactionIID := consensushashing.TransactionID(acceptedTransactions[i])
			acceptedTransactionJID := consensushashing.TransactionID(acceptedTransactions[j])
			return acceptedTransactionIID.Less(acceptedTransactionJID)
		})
	}

	return merkle.CalculateIDMerkleRoot(acceptedTransactions), nil
}

func (bb *blockBuilder) newBlockUTXOCommitment(stagingArea *model.StagingArea) (*externalapi.DomainHash, error) {
	newBlockMultiset, err := bb.multisetStore.Get(bb.databaseContext, stagingArea, model.VirtualBlockHash)
	if database.IsNotFoundError(err) {
		log.Infof("newBlockUTXOCommitment failed to retrieve with %s\n", model.VirtualBlockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	newBlockUTXOCommitment := newBlockMultiset.Hash()
	return newBlockUTXOCommitment, nil
}

func (bb *blockBuilder) newBlockDAAScore(stagingArea *model.StagingArea) (uint64, error) {
	return bb.daaBlocksStore.DAAScore(bb.databaseContext, stagingArea, model.VirtualBlockHash)
}

func (bb *blockBuilder) newBlockBlueWork(stagingArea *model.StagingArea) (*big.Int, error) {
	virtualGHOSTDAGData, err := bb.ghostdagDataStore.Get(bb.databaseContext, stagingArea, model.VirtualBlockHash, false)

	if database.IsNotFoundError(err) {
		log.Infof("newBlockBlueWork failed to retrieve with %s\n", model.VirtualBlockHash)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return virtualGHOSTDAGData.BlueWork(), nil
}

func (bb *blockBuilder) newBlockBlueScore(stagingArea *model.StagingArea) (uint64, error) {
	virtualGHOSTDAGData, err := bb.ghostdagDataStore.Get(bb.databaseContext, stagingArea, model.VirtualBlockHash, false)
	if database.IsNotFoundError(err) {
		log.Infof("newBlockBlueScore failed to retrieve with %s\n", model.VirtualBlockHash)
		return 0, err
	}
	if err != nil {
		return 0, err
	}
	return virtualGHOSTDAGData.BlueScore(), nil
}

func (bb *blockBuilder) newBlockPruningPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	return bb.pruningManager.ExpectedHeaderPruningPoint(stagingArea, blockHash)
}
