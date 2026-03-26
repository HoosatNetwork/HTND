package blockvalidator_test

import (
	"testing"
	"time"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
	"github.com/pkg/errors"
)

func TestCheckBlockIsNotPruned(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		// This is done to reduce the pruning depth to 6 blocks
		consensusConfig.FinalityDuration = []time.Duration{2 * consensusConfig.TargetTimePerBlock[constants.GetBlockVersion()-1]}
		consensusConfig.K[constants.GetBlockVersion()-1] = 0

		// When pruning, blocks in the DAA window of the pruning point and its
		// anticone are kept for the sake of IBD. Setting this value to zero
		// forces all DAA windows to be empty, and as such, no blocks are kept
		// below the pruning point
		consensusConfig.DifficultyAdjustmentWindowSize = []int{0}

		factory := consensus.NewFactory()

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestCheckBlockIsNotPruned")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		// Add blocks until the pruning point changes
		tipHash := consensusConfig.GenesisHash
		tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		beforePruningBlock, _, err := tc.GetBlock(tipHash)
		if err != nil {
			t.Fatalf("beforePruningBlock: %+v", err)
		}

		for {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}

			pruningPoint, err := tc.PruningPoint()
			if err != nil {
				t.Fatalf("PruningPoint: %+v", err)
			}

			if !pruningPoint.Equal(consensusConfig.GenesisHash) {
				break
			}
		}

		err = tc.ValidateAndInsertBlock(beforePruningBlock, true, true)
		if !errors.Is(err, ruleerrors.ErrPrunedBlock) {
			t.Fatalf("Unexpected error: %+v", err)
		}

		beforePruningBlockBlockStatus, err := tc.BlockStatusStore().Get(tc.DatabaseContext(), model.NewStagingArea(),
			consensushashing.BlockHash(beforePruningBlock))
		if err != nil {
			t.Fatalf("BlockStatusStore().Get: %+v", err)
		}

		// Check that the block still has header only status although it got rejected.
		if beforePruningBlockBlockStatus != externalapi.StatusHeaderOnly {
			t.Fatalf("Unexpected status %s", beforePruningBlockBlockStatus)
		}
	})
}

func TestCheckParentBlockBodiesExist(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		// This is done to reduce the pruning depth to 6 blocks
		consensusConfig.FinalityDuration = []time.Duration{2 * consensusConfig.TargetTimePerBlock[constants.GetBlockVersion()-1]}
		consensusConfig.K[constants.GetBlockVersion()-1] = 0

		factory := consensus.NewFactory()

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestCheckParentBlockBodiesExist")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		headerHash, _, err := tc.AddUTXOInvalidHeader([]*externalapi.DomainHash{consensusConfig.GenesisHash})
		if err != nil {
			t.Fatalf("AddUTXOInvalidHeader: %+v", err)
		}

		_, _, err = tc.AddUTXOInvalidBlock([]*externalapi.DomainHash{headerHash})
		errMissingParents := &ruleerrors.ErrMissingParents{}
		if !errors.As(err, errMissingParents) {
			t.Fatalf("Unexpected error: %+v", err)
		}

		if !externalapi.HashesEqual(errMissingParents.MissingParentHashes, []*externalapi.DomainHash{headerHash}) {
			t.Fatalf("unexpected missing parents %s", errMissingParents.MissingParentHashes)
		}

		// Add blocks until the pruning point changes
		tipHash := consensusConfig.GenesisHash
		anticonePruningBlock, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{tipHash}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}

		// Add only the header of anticonePruningBlock
		err = tc.ValidateAndInsertBlock(&externalapi.DomainBlock{
			Header:       anticonePruningBlock.Header,
			Transactions: nil,
		}, true, true)
		if err != nil {
			t.Fatalf("ValidateAndInsertBlock: %+v", err)
		}

		for {
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("AddUTXOInvalidHeader: %+v", err)
			}

			pruningPoint, err := tc.PruningPoint()
			if err != nil {
				t.Fatalf("PruningPoint: %+v", err)
			}

			if !pruningPoint.Equal(consensusConfig.GenesisHash) {
				break
			}
		}

		// Add anticonePruningBlock's body and check that it's valid to point to
		// a header only block in the past of the pruning point.
		err = tc.ValidateAndInsertBlock(anticonePruningBlock, true, true)
		if err != nil {
			t.Fatalf("ValidateAndInsertBlock: %+v", err)
		}
	})
}

func TestIsFinalizedTransaction(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		consensusConfig.BlockCoinbaseMaturity = 0
		for i := range consensusConfig.DifficultyAdjustmentWindowSize {
			consensusConfig.DifficultyAdjustmentWindowSize[i] = 1
		}
		factory := consensus.NewFactory()

		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestIsFinalizedTransaction")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		fundingParentHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		bootstrapTransaction := testutils.CreateTransactionWithOutput(10)
		err = testutils.StageTransactionOutputsToVirtual(tc, bootstrapTransaction, 0)
		if err != nil {
			t.Fatalf("StageTransactionOutputsToVirtual: %+v", err)
		}

		fundingTransaction, err := testutils.CreateTransaction(bootstrapTransaction, 1)
		if err != nil {
			t.Fatalf("Error creating funding transaction: %+v", err)
		}

		fundingBlockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{fundingParentHash}, nil, []*externalapi.DomainTransaction{fundingTransaction})
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}

		fundingBlock, _, err := tc.GetBlock(fundingBlockHash)
		if err != nil {
			t.Fatalf("Error getting funding block: %+v", err)
		}
		fundingTransaction = fundingBlock.Transactions[1]

		candidateBlock, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{fundingBlockHash}, nil, nil)
		if err != nil {
			t.Fatalf("BuildBlockWithParents: %+v", err)
		}
		candidateBlockDAAScore := candidateBlock.Header.DAAScore()

		var tempHash externalapi.DomainHash
		tc.BlockRelationStore().StageBlockRelation(stagingArea, &tempHash, &model.BlockRelations{
			Parents:  []*externalapi.DomainHash{fundingBlockHash},
			Children: nil,
		})

		err = tc.GHOSTDAGManager().GHOSTDAG(stagingArea, &tempHash)
		if err != nil {
			t.Fatalf("GHOSTDAG: %+v", err)
		}
		candidatePastMedianTime, err := tc.PastMedianTimeManager().PastMedianTime(stagingArea, &tempHash)
		if err != nil {
			t.Fatalf("PastMedianTime: %+v", err)
		}

		checkForLockTimeAndSequence := func(lockTime, sequence uint64, shouldPass bool) {
			tx, err := testutils.CreateTransaction(fundingTransaction, 1)
			if err != nil {
				t.Fatalf("Error creating tx: %+v", err)
			}

			tx.LockTime = lockTime
			tx.Inputs[0].Sequence = sequence

			_, _, err = tc.AddBlock([]*externalapi.DomainHash{fundingBlockHash}, nil, []*externalapi.DomainTransaction{tx})
			if (shouldPass && err != nil) || (!shouldPass && !errors.Is(err, ruleerrors.ErrUnfinalizedTx)) {
				t.Fatalf("shouldPass: %t Unexpected error: %+v", shouldPass, err)
			}
		}

		// Check that the same DAAScore or higher fails, but lower passes.
		checkForLockTimeAndSequence(candidateBlockDAAScore+1, 0, false)
		checkForLockTimeAndSequence(candidateBlockDAAScore, 0, false)
		checkForLockTimeAndSequence(candidateBlockDAAScore-1, 0, true)

		// Check that the same pastMedianTime or higher fails, but lower passes.
		checkForLockTimeAndSequence(uint64(candidatePastMedianTime)+1, 0, false)
		checkForLockTimeAndSequence(uint64(candidatePastMedianTime), 0, false)
		checkForLockTimeAndSequence(uint64(candidatePastMedianTime)-1, 0, true)

		// We check that if the transaction is marked as finalized it'll pass for any lock time.
		checkForLockTimeAndSequence(uint64(candidatePastMedianTime), constants.MaxTxInSequenceNum, true)
		checkForLockTimeAndSequence(2, constants.MaxTxInSequenceNum, true)
	})
}
