package blockbuilder

import (
	"sort"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/pkg/errors"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/hashes"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/merkle"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/util/mstime"
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

	// Everything the header commits to - the coinbase's outputs, the accepted-ID merkle root, the
	// UTXO commitment, the DAA score, the blue work - is a function of the block's own merge set, and
	// a validator derives every one of them from the block in front of it. Deriving them from virtual
	// instead only agrees for as long as virtual and the new block answer the same questions the same
	// way, and they do not have to: calcMergedBlockReward pays a merge set block only if it is in the
	// DAA added blocks set of the block the coinbase is being built for, and the acceptance data
	// decides which merge set block a fee is credited to. Both are asked of virtual here and of the
	// new block at validation, which is why a block this node builds can be rejected by this node
	// with "Output count differs" or with a coinbase short by exactly one transaction's fee.
	//
	// Stage the prospective block under a temporary hash and ask every one of those questions about
	// it, the way the test builder already does. When virtual and the new block agree - the normal
	// case - the result is identical to before. The staging area belongs to this build and is never
	// committed, so nothing of the temporary block is written to the database; see
	// prospectiveBlockHash for why the name it is staged under still has to mean something.
	prospectiveBlockHash, parents, bits, err := bb.stageProspectiveBlock(stagingArea)
	if err != nil {
		return nil, false, err
	}

	newBlockDAAScore, err := bb.daaBlocksStore.DAAScore(bb.databaseContext, stagingArea, prospectiveBlockHash)
	if err != nil {
		return nil, false, err
	}
	blockVersion := bb.blockVersionForDAAScore(newBlockDAAScore)
	constants.SetBlockVersion(blockVersion)

	newBlockPruningPoint, err := bb.newBlockPruningPoint(stagingArea, prospectiveBlockHash)
	if err != nil {
		return nil, false, err
	}

	newBlockTimeInMilliseconds, err := bb.newBlockTime(stagingArea, prospectiveBlockHash)
	if err != nil {
		return nil, false, err
	}

	// One replay, same snapshot for coinbase + accepted-ID root + UTXO commitment.
	// Do not mix a fresh acceptance row with a stale multiset: validation
	// derives all three from CalculatePastUTXOAndAcceptanceData(H).
	_, acceptanceData, multiset, err := bb.consensusStateManager.CalculatePastUTXOAndAcceptanceData(
		stagingArea, prospectiveBlockHash)
	if err != nil {
		return nil, false, err
	}
	bb.acceptanceDataStore.Stage(stagingArea, prospectiveBlockHash, acceptanceData)
	bb.multisetStore.Stage(stagingArea, prospectiveBlockHash, multiset)

	coinbase, coinbaseHasRedReward, err := bb.newBlockCoinbaseTransaction(
		stagingArea, prospectiveBlockHash, coinbaseData, newBlockTimeInMilliseconds)
	if err != nil {
		return nil, false, err
	}
	transactionsWithCoinbase := append([]*externalapi.DomainTransaction{coinbase}, transactions...)

	header, err := bb.buildHeader(stagingArea, prospectiveBlockHash, parents, bits, newBlockDAAScore, blockVersion,
		transactionsWithCoinbase, acceptanceData, multiset, newBlockPruningPoint, newBlockTimeInMilliseconds)
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
	blockHash *externalapi.DomainHash, coinbaseData *externalapi.DomainCoinbaseData, candidateTimestamp int64,
) (*externalapi.DomainTransaction, bool, error) {
	return bb.coinbaseManager.ExpectedCoinbaseTransaction(
		stagingArea, blockHash, coinbaseData, candidateTimestamp)
}

// prospectiveBlockHash returns the temporary hash a prospective block with these parents is staged
// under. It is derived from the parents rather than being a counter, and that is not cosmetic:
// stores are read through per-store LRU caches that are keyed by block hash and are filled from the
// staging area on a plain read (blockwindowheapslicestore.Get does exactly this), so anything
// computed for a temporary hash outlives the staging area it was staged in. A name that means
// "these parents" keeps every such cached entry true - a later build that reaches the same name has
// the same parents and so the same window, GHOSTDAG data and DAA data - while a counter would hand
// the same name to a different DAG on the next call, and would collide with the test builder's
// temporary hashes, which are counters over these same stores.
func prospectiveBlockHash(parents []*externalapi.DomainHash) *externalapi.DomainHash {
	hashWriter := hashes.NewBlockHashWriter()
	// A tag no block header starts with, so this can never be read as a real block's preimage.
	hashWriter.InfallibleWrite([]byte("prospective-block"))
	for _, parent := range parents {
		hashWriter.InfallibleWrite(parent.ByteSlice())
	}
	return hashWriter.Finalize()
}

// stageProspectiveBlock picks the parents of the block about to be built and stages it, under a
// temporary hash, as far as everything downstream needs: its relations, its GHOSTDAG data and its
// DAA data. It returns that hash, the parents for the header, and the difficulty the staged DAA
// window requires.
//
// The parents are still chosen from virtual's, which is what makes the new block virtual's child.
// What changes is that from here on the block is a block of its own, so its merge set, its DAA added
// blocks and its past are the ones every later question is answered against - the same ones the
// validator will use.
func (bb *blockBuilder) stageProspectiveBlock(stagingArea *model.StagingArea) (
	*externalapi.DomainHash, []externalapi.BlockLevelParents, uint32, error,
) {
	// Choosing the parents needs a DAA score and the block does not have one yet, so virtual's is
	// used for that one decision, as it was before. It only selects the parent-building rule, and
	// the prospective block's own DAA score follows from the parents this returns.
	virtualDAAScore, err := bb.newBlockDAAScore(stagingArea)
	if err != nil {
		return nil, nil, 0, err
	}
	parents, err := bb.newBlockParents(stagingArea, virtualDAAScore)
	if err != nil {
		return nil, nil, 0, err
	}
	if len(parents) == 0 || len(parents[0]) == 0 {
		return nil, nil, 0, errors.Errorf("cannot build a block over virtual: it has no direct parents")
	}

	prospectiveBlockHash := prospectiveBlockHash(parents[0])
	bb.blockRelationStore.StageBlockRelation(stagingArea, prospectiveBlockHash,
		&model.BlockRelations{Parents: parents[0]})

	err = bb.ghostdagManager.GHOSTDAG(stagingArea, prospectiveBlockHash)
	if err != nil {
		return nil, nil, 0, err
	}

	// Stages both the DAA score and the DAA added blocks set - the set calcMergedBlockReward asks
	// whether a merge set block is in before it pays it anything.
	bits, err := bb.difficultyManager.StageDAADataAndReturnRequiredDifficulty(stagingArea, prospectiveBlockHash, false)
	if err != nil {
		return nil, nil, 0, err
	}

	return prospectiveBlockHash, parents, bits, nil
}

// buildHeader assembles the header of the prospective block from what was staged and calculated for
// that same block, so that every commitment in it describes the block it is the header of.
// acceptanceData and multiset are the ones the coinbase was built from, passed in rather than read
// back, so the three cannot come from different replays.
func (bb *blockBuilder) buildHeader(stagingArea *model.StagingArea, prospectiveBlockHash *externalapi.DomainHash,
	parents []externalapi.BlockLevelParents, bits uint32, daaScore uint64, blockVersion uint16,
	transactions []*externalapi.DomainTransaction, acceptanceData externalapi.AcceptanceData, multiset model.Multiset,
	newBlockPruningPoint *externalapi.DomainHash, timeInMilliseconds int64,
) (externalapi.BlockHeader, error) {
	hashMerkleRoot := bb.newBlockHashMerkleRoot(transactions)
	acceptedIDMerkleRoot, err := bb.calculateAcceptedIDMerkleRoot(acceptanceData, blockVersion)
	if err != nil {
		return nil, err
	}
	utxoCommitment := multiset.Hash()

	ghostdagData, err := bb.ghostdagDataStore.Get(bb.databaseContext, stagingArea, prospectiveBlockHash, false)
	if err != nil {
		return nil, err
	}
	blueWork := ghostdagData.BlueWork()
	blueScore := ghostdagData.BlueScore()

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

func (bb *blockBuilder) newBlockTime(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (int64, error) {
	// The timestamp for the block must not be before the median timestamp
	// of the last several blocks. Thus, choose the maximum between the
	// current time and one second after the past median time. The current
	// timestamp is truncated to a millisecond boundary before comparison since a
	// block timestamp does not supported a precision greater than one
	// millisecond.
	newTimestamp := mstime.Now().UnixMilliseconds()
	minTimestamp, err := bb.minBlockTime(stagingArea, blockHash)
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

func (bb *blockBuilder) newBlockHashMerkleRoot(transactions []*externalapi.DomainTransaction) *externalapi.DomainHash {
	return merkle.CalculateHashMerkleRoot(transactions)
}

func (bb *blockBuilder) calculateAcceptedIDMerkleRoot(acceptanceData externalapi.AcceptanceData, blockVersion uint16) (*externalapi.DomainHash, error) {
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
	if blockVersion < 5 {
		sort.Slice(acceptedTransactions, func(i, j int) bool {
			acceptedTransactionIID := consensushashing.TransactionID(acceptedTransactions[i])
			acceptedTransactionJID := consensushashing.TransactionID(acceptedTransactions[j])
			return acceptedTransactionIID.Less(acceptedTransactionJID)
		})
	}

	return merkle.CalculateIDMerkleRoot(acceptedTransactions), nil
}

func (bb *blockBuilder) newBlockDAAScore(stagingArea *model.StagingArea) (uint64, error) {
	return bb.daaBlocksStore.DAAScore(bb.databaseContext, stagingArea, model.VirtualBlockHash)
}

func (bb *blockBuilder) newBlockPruningPoint(stagingArea *model.StagingArea, blockHash *externalapi.DomainHash) (*externalapi.DomainHash, error) {
	return bb.pruningManager.ExpectedHeaderPruningPoint(stagingArea, blockHash)
}
