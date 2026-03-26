package blockvalidator_test

import (
	"reflect"
	"runtime"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus/model/testapi"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/ruleerrors"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/blockheader"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
	"github.com/Hoosat-Oy/HTND/util/mstime"
	"github.com/pkg/errors"
)

func TestBlockValidator_ValidateHeaderInIsolation(t *testing.T) {
	tests := []func(t *testing.T, tc testapi.TestConsensus, cfg *consensus.Config){
		CheckParentsLimit,
		CheckBlockVersion,
		CheckBlockTimestampInIsolation,
	}
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		tc, teardown, err := consensus.NewFactory().NewTestConsensus(consensusConfig, "TestBlockValidator_ValidateHeaderInIsolation")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)
		for _, test := range tests {
			testName := runtime.FuncForPC(reflect.ValueOf(test).Pointer()).Name()
			t.Run(testName, func(t *testing.T) {
				test(t, tc, consensusConfig)
			})
		}
	})
}

func CheckParentsLimit(t *testing.T, tc testapi.TestConsensus, consensusConfig *consensus.Config) {
	parents := make([]*externalapi.DomainHash, 0, consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1]+1)
	for i := externalapi.KType(0); i < consensusConfig.MaxBlockParents[constants.GetBlockVersion()-1]+1; i++ {
		blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
		if err != nil {
			t.Fatalf("AddBlock: %+v", err)
		}
		parents = append(parents, blockHash)
	}

	block, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
	if err != nil {
		t.Fatalf("BuildBlockWithParents: %+v", err)
	}

	block.Header = blockheader.NewImmutableBlockHeader(
		block.Header.Version(),
		[]externalapi.BlockLevelParents{parents},
		block.Header.HashMerkleRoot(),
		block.Header.AcceptedIDMerkleRoot(),
		block.Header.UTXOCommitment(),
		block.Header.TimeInMilliseconds(),
		block.Header.Bits(),
		block.Header.Nonce(),
		block.Header.DAAScore(),
		block.Header.BlueScore(),
		block.Header.BlueWork(),
		block.Header.PruningPoint(),
	)

	err = tc.ValidateAndInsertBlock(block, true, true)
	if !errors.Is(err, ruleerrors.ErrTooManyParents) {
		t.Fatalf("Unexpected error: %+v", err)
	}
}

func CheckBlockVersion(t *testing.T, tc testapi.TestConsensus, consensusConfig *consensus.Config) {
	block, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{consensusConfig.GenesisHash}, nil, nil)
	if err != nil {
		t.Fatalf("BuildBlockWithParents: %+v", err)
	}

	expectedVersion := constants.GetBlockVersion()
	block.Header = blockheader.NewImmutableBlockHeader(
		expectedVersion+1,
		block.Header.Parents(),
		block.Header.HashMerkleRoot(),
		block.Header.AcceptedIDMerkleRoot(),
		block.Header.UTXOCommitment(),
		block.Header.TimeInMilliseconds(),
		block.Header.Bits(),
		block.Header.Nonce(),
		block.Header.DAAScore(),
		block.Header.BlueScore(),
		block.Header.BlueWork(),
		block.Header.PruningPoint(),
	)

	err = tc.ValidateAndInsertBlock(block, true, true)
	if !errors.Is(err, ruleerrors.ErrWrongBlockVersion) {
		t.Fatalf("Unexpected error: %+v", err)
	}
}

func CheckBlockTimestampInIsolation(t *testing.T, tc testapi.TestConsensus, cfg *consensus.Config) {
	block, _, err := tc.BuildBlockWithParents([]*externalapi.DomainHash{cfg.GenesisHash}, nil, nil)
	if err != nil {
		t.Fatalf("BuildBlockWithParents: %+v", err)
	}

	// Give 10 seconds slack to take care of the test duration
	timestamp := mstime.Now().UnixMilliseconds() +
		int64(cfg.TimestampDeviationTolerance)*cfg.TargetTimePerBlock[constants.GetBlockVersion()-1].Milliseconds() + 10_000

	block.Header = blockheader.NewImmutableBlockHeader(
		block.Header.Version(),
		block.Header.Parents(),
		block.Header.HashMerkleRoot(),
		block.Header.AcceptedIDMerkleRoot(),
		block.Header.UTXOCommitment(),
		timestamp,
		block.Header.Bits(),
		block.Header.Nonce(),
		block.Header.DAAScore(),
		block.Header.BlueScore(),
		block.Header.BlueWork(),
		block.Header.PruningPoint(),
	)

	err = tc.ValidateAndInsertBlock(block, true, true)
	if !errors.Is(err, ruleerrors.ErrTimeTooMuchInTheFuture) {
		t.Fatalf("Unexpected error: %+v", err)
	}
}
