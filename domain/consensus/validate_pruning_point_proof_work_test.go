package consensus_test

import (
	"math"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/ruleerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// TestValidatePruningPointProofRequiresProofWork pins that a proof sharing no blocks with the current
// DAG is accepted only if some level of the proof itself reaches 2m blue blocks. Without that, the
// final comparison looked at the current DAG alone, and a one-header proof with no proof of work was
// accepted by a fresh node and by one whose pruning point had moved. An honest proof is still accepted.
func TestValidatePruningPointProofRequiresProofWork(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		if !cfg.GenesisHash.Equal(dagconfig.MainnetParams.GenesisHash) {
			t.Skip("the proof-side blue-work check runs only on mainnet")
		}
		finalityDepth := 5
		cfg.FinalityDuration = []time.Duration{time.Duration(finalityDepth) * cfg.TargetTimePerBlock[constants.GetBlockVersion()-1]}
		cfg.K[constants.GetBlockVersion()-1] = 0
		cfg.PruningProofM = 1
		cfg.POWScores = []uint64{math.MaxUint64}

		factory := consensus.NewFactory()
		syncer, teardownSyncer, err := factory.NewTestConsensus(cfg, "TestValidatePruningPointProofRequiresProofWorkSyncer")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownSyncer(false)
		fresh, teardownFresh, err := factory.NewTestConsensus(cfg, "TestValidatePruningPointProofRequiresProofWorkFresh")
		if err != nil {
			t.Fatalf("NewTestConsensus: %+v", err)
		}
		defer teardownFresh(false)

		tip := cfg.GenesisHash
		for i := 0; i < 200; i++ {
			tip, _, err = syncer.AddBlock([]*externalapi.DomainHash{tip}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
		}
		pruningPoint, err := syncer.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		if pruningPoint.Equal(cfg.GenesisHash) {
			t.Fatalf("the syncer's pruning point did not move")
		}

		honest, err := syncer.BuildPruningPointProof()
		if err != nil {
			t.Fatalf("BuildPruningPointProof: %+v", err)
		}
		if err := fresh.ValidatePruningPointProof(honest); err != nil {
			t.Fatalf("a fresh node refused an honest proof: %+v", err)
		}

		// One level-0 header with an unknown parent and no proof of work at all.
		genesis := cfg.GenesisBlock.Header
		var unknownParent [32]byte
		unknownParent[0] = 0x42
		forged := blockheader.NewImmutableBlockHeader(genesis.Version(),
			[]externalapi.BlockLevelParents{{externalapi.NewDomainHashFromByteArray(&unknownParent)}},
			genesis.HashMerkleRoot(), genesis.AcceptedIDMerkleRoot(), genesis.UTXOCommitment(),
			genesis.TimeInMilliseconds()+1000, genesis.Bits(), 7, 1, 1, genesis.BlueWork(), genesis.PruningPoint())
		levels := make([][]externalapi.BlockHeader, cfg.MaxBlockLevel+1)
		levels[0] = []externalapi.BlockHeader{forged}
		forgedProof := &externalapi.PruningPointProof{Headers: levels}

		for name, tc := range map[string]interface {
			ValidatePruningPointProof(*externalapi.PruningPointProof) error
		}{"fresh": fresh, "moved pruning point": syncer} {
			err := tc.ValidatePruningPointProof(forgedProof)
			if !errors.Is(err, ruleerrors.ErrPruningProofInsufficientBlueWork) {
				t.Errorf("%s node: expected ErrPruningProofInsufficientBlueWork for a one-header proof, got %v", name, err)
			}
		}
	})
}
