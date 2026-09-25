package blockrelay

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/HoosatNetwork/HTND/v2/app/appmessage"
	"github.com/HoosatNetwork/HTND/v2/app/protocol/protocolerrors"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/dagconfig"
	"github.com/pkg/errors"
)

// buildPruningDAG mines rounds of up to width parallel blocks on random subsets of the tips, at block
// version 10, until the pruning point has moved for 40 rounds.
func buildPruningDAG(t *testing.T, tc testapi.TestConsensus, genesis *externalapi.DomainHash, seed int64, width int) {
	rng := rand.New(rand.NewSource(seed))
	movedRounds := 0
	for round := 0; movedRounds < 40; round++ {
		if round > 3000 {
			t.Fatalf("seed %d: the pruning point never moved", seed)
		}
		tips, err := tc.Tips()
		if err != nil {
			t.Fatalf("Tips: %+v", err)
		}
		blocks := 1 + rng.Intn(width)
		for i := 0; i < blocks; i++ {
			perm := rng.Perm(len(tips))
			parents := make([]*externalapi.DomainHash, 0, width)
			for _, index := range perm[:1+rng.Intn(min(width, len(tips)))] {
				parents = append(parents, tips[index])
			}
			block, _, err := tc.BuildBlockWithParents(parents, nil, nil)
			if err != nil {
				t.Fatalf("BuildBlockWithParents: %+v", err)
			}
			err = tc.ValidateAndInsertBlock(block, true, true)
			if err != nil {
				t.Fatalf("ValidateAndInsertBlock: %+v", err)
			}
		}
		err = tc.ResolveVirtual(nil)
		if err != nil {
			t.Fatalf("ResolveVirtual: %+v", err)
		}
		pruningPoint, err := tc.PruningPoint()
		if err != nil {
			t.Fatalf("PruningPoint: %+v", err)
		}
		if !pruningPoint.Equal(genesis) {
			movedRounds++
		}
	}
}

func blockWithTrustedDataFrom(t *testing.T, syncer testapi.TestConsensus, blockHash *externalapi.DomainHash) *externalapi.BlockWithTrustedData {
	block, _, err := syncer.GetBlock(blockHash)
	if err != nil {
		t.Fatalf("GetBlock: %+v", err)
	}
	daaWindowHashes, err := syncer.BlockDAAWindowHashes(blockHash)
	if err != nil {
		t.Fatalf("BlockDAAWindowHashes: %+v", err)
	}
	ghostdagDataHashes, err := syncer.TrustedBlockAssociatedGHOSTDAGDataBlockHashes(blockHash)
	if err != nil {
		t.Fatalf("TrustedBlockAssociatedGHOSTDAGDataBlockHashes: %+v", err)
	}
	result := &externalapi.BlockWithTrustedData{Block: block}
	for i, daaBlockHash := range daaWindowHashes {
		header, err := syncer.TrustedDataDataDAAHeader(blockHash, daaBlockHash, uint64(uint(i)))
		if err != nil {
			t.Fatalf("TrustedDataDataDAAHeader: %+v", err)
		}
		result.DAAWindow = append(result.DAAWindow, header)
	}
	for _, ghostdagDataHash := range ghostdagDataHashes {
		data, err := syncer.TrustedGHOSTDAGData(ghostdagDataHash)
		if err != nil {
			t.Fatalf("TrustedGHOSTDAGData: %+v", err)
		}
		result.GHOSTDAGData = append(result.GHOSTDAGData, &externalapi.BlockGHOSTDAGDataHashPair{Hash: ghostdagDataHash, GHOSTDAGData: data})
	}
	return result
}

// stageHeadersProofSync does the part of a headers-proof IBD that runs before the pruning point is
// accepted: apply the proof, import the pruning points, insert the pruning point and then its
// anticone in anticoneOrder, and insert every header up to the syncer's headers selected tip.
func stageHeadersProofSync(t *testing.T, factory consensus.Factory, cfg *consensus.Config, syncer testapi.TestConsensus,
	name string, anticoneOrder func([]*externalapi.DomainHash) []*externalapi.DomainHash,
) (testapi.TestConsensus, func(bool)) {
	proof, err := syncer.BuildPruningPointProof()
	if err != nil {
		t.Fatalf("BuildPruningPointProof: %+v", err)
	}
	stagingConfig := *cfg
	stagingConfig.SkipAddingGenesis = true
	syncee, teardown, err := factory.NewTestConsensus(&stagingConfig, name)
	if err != nil {
		t.Fatalf("NewTestConsensus: %+v", err)
	}
	constants.ForceSetBlockVersion(10)
	if err := syncee.ApplyPruningPointProof(proof); err != nil {
		t.Fatalf("ApplyPruningPointProof: %+v", err)
	}
	pruningPointHeaders, err := syncer.PruningPointHeaders()
	if err != nil {
		t.Fatalf("PruningPointHeaders: %+v", err)
	}
	if err := syncee.ImportPruningPoints(pruningPointHeaders); err != nil {
		t.Fatalf("ImportPruningPoints: %+v", err)
	}
	pruningPointAndItsAnticone, err := syncer.PruningPointAndItsAnticone()
	if err != nil {
		t.Fatalf("PruningPointAndItsAnticone: %+v", err)
	}
	ordered := append([]*externalapi.DomainHash{pruningPointAndItsAnticone[0]}, anticoneOrder(pruningPointAndItsAnticone[1:])...)
	for _, blockHash := range ordered {
		if err := syncee.ValidateAndInsertBlockWithTrustedData(blockWithTrustedDataFrom(t, syncer, blockHash), false); err != nil {
			t.Fatalf("ValidateAndInsertBlockWithTrustedData: %+v", err)
		}
	}
	pruningPoint, _ := syncer.PruningPoint()
	headersSelectedTip, _ := syncer.GetHeadersSelectedTip()
	hashes, _, err := syncer.GetHashesBetween(pruningPoint, headersSelectedTip, math.MaxUint64, false)
	if err != nil {
		t.Fatalf("GetHashesBetween: %+v", err)
	}
	for _, blockHash := range hashes {
		info, err := syncee.GetBlockInfo(blockHash)
		if err != nil {
			t.Fatalf("GetBlockInfo: %+v", err)
		}
		if info.Exists {
			continue
		}
		header, err := syncer.GetBlockHeader(blockHash)
		if err != nil {
			t.Fatalf("GetBlockHeader: %+v", err)
		}
		if err := syncee.ValidateAndInsertBlock(&externalapi.DomainBlock{Header: header}, false, true); err != nil {
			t.Fatalf("ValidateAndInsertBlock header: %+v", err)
		}
	}
	return syncee, teardown
}

// TestPruningPointThatMissesTheChainIsRefused pins the acceptance guard of HTN-196 on a real consensus
// in the broken state. A pruning point anticone inserted children-first - what a peer ordering by
// unvalidated header blue work could hand a node that does not reorder it - leaves these seeds'
// pruning point off the selected chain of the syncer's tip, the state that used to be committed and
// then stuck with no block bodies. The guard the IBD flow runs before committing must refuse it
// without banning, and must accept the same DAG synced in parent-first order.
func TestPruningPointThatMissesTheChainIsRefused(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, cfg *consensus.Config) {
		if cfg.Name != dagconfig.MainnetParams.Name {
			return
		}
		n := len(cfg.TargetTimePerBlock)
		cfg.FinalityDuration = make([]time.Duration, n)
		cfg.K = make([]externalapi.KType, n)
		for i := 0; i < n; i++ {
			cfg.FinalityDuration[i] = 15 * cfg.TargetTimePerBlock[i]
			cfg.K[i] = 6
		}
		cfg.MergeSetSizeLimit = 10
		cfg.PruningProofM = 40
		cfg.POWScores = make([]uint64, len(cfg.POWScores))
		for i := range cfg.POWScores {
			cfg.POWScores[i] = 1
		}
		factory := consensus.NewFactory()

		childrenFirst := func(anticone []*externalapi.DomainHash) []*externalapi.DomainHash {
			reversed := make([]*externalapi.DomainHash, len(anticone))
			for i, blockHash := range anticone {
				reversed[len(anticone)-1-i] = blockHash
			}
			return reversed
		}

		for _, seed := range []int64{1, 13} {
			syncer, teardownSyncer, err := factory.NewTestConsensus(cfg, fmt.Sprintf("RefusePruningPointSyncer%d", seed))
			if err != nil {
				t.Fatalf("NewTestConsensus: %+v", err)
			}
			constants.ForceSetBlockVersion(10)
			buildPruningDAG(t, syncer, cfg.GenesisHash, seed, 5)
			pruningPoint, err := syncer.PruningPoint()
			if err != nil {
				t.Fatalf("PruningPoint: %+v", err)
			}
			syncerTip, err := syncer.GetHeadersSelectedTip()
			if err != nil {
				t.Fatalf("GetHeadersSelectedTip: %+v", err)
			}

			// Out of order: the broken state must actually be there, or this test proves nothing.
			broken, teardownBroken := stageHeadersProofSync(t, factory, cfg, syncer,
				fmt.Sprintf("RefusePruningPointBroken%d", seed), childrenFirst)
			isOnChain, err := broken.IsInSelectedParentChainOf(pruningPoint, syncerTip)
			if err != nil {
				t.Fatalf("IsInSelectedParentChainOf: %+v", err)
			}
			if isOnChain {
				t.Fatalf("seed %d: the children-first sync did not leave the pruning point off the tip's chain, so "+
					"the refusal is not exercised", seed)
			}
			err = checkPruningPointMeetsChains(broken, pruningPoint, []namedBlock{{name: "the syncer's headers selected tip", hash: syncerTip}})
			protocolErr := &protocolerrors.ProtocolError{}
			if !errors.As(err, protocolErr) {
				t.Fatalf("seed %d: expected the pruning point to be refused with a protocol error, got: %v", seed, err)
			}
			if protocolErr.ShouldBan {
				t.Fatalf("seed %d: refusing the pruning point must not ban the peer", seed)
			}
			teardownBroken(false)

			// In the order the IBD flow now inserts them, the same DAG is accepted.
			parentsFirst := func(anticone []*externalapi.DomainHash) []*externalapi.DomainHash {
				messages := make([]*appmessage.MsgBlockWithTrustedDataV4, 0, len(anticone))
				byHash := make(map[externalapi.DomainHash]*externalapi.DomainHash, len(anticone))
				for _, blockHash := range childrenFirst(anticone) {
					block, _, err := syncer.GetBlock(blockHash)
					if err != nil {
						t.Fatalf("GetBlock: %+v", err)
					}
					messages = append(messages, &appmessage.MsgBlockWithTrustedDataV4{Block: appmessage.DomainBlockToMsgBlock(block)})
					byHash[*blockHash] = blockHash
				}
				ordered := make([]*externalapi.DomainHash, 0, len(anticone))
				for _, message := range orderBlocksWithTrustedDataTopologically(messages) {
					ordered = append(ordered, byHash[*consensushashing.BlockHash(appmessage.MsgBlockToDomainBlock(message.Block))])
				}
				return ordered
			}
			fixed, teardownFixed := stageHeadersProofSync(t, factory, cfg, syncer,
				fmt.Sprintf("RefusePruningPointFixed%d", seed), parentsFirst)
			synceeTip, err := fixed.GetHeadersSelectedTip()
			if err != nil {
				t.Fatalf("GetHeadersSelectedTip: %+v", err)
			}
			err = checkPruningPointMeetsChains(fixed, pruningPoint, []namedBlock{
				{name: "the syncer's headers selected tip", hash: syncerTip},
				{name: "this node's headers selected tip", hash: synceeTip},
			})
			if err != nil {
				t.Fatalf("seed %d: the pruning point of a correctly ordered sync was refused: %v", seed, err)
			}
			teardownFixed(false)
			teardownSyncer(false)
		}
	})
}

type fakeOwnPruningPoint struct {
	pruningPoint, headersSelectedTip *externalapi.DomainHash
	onChain                          bool
}

func (f *fakeOwnPruningPoint) PruningPoint() (*externalapi.DomainHash, error) {
	return f.pruningPoint, nil
}
func (f *fakeOwnPruningPoint) GetHeadersSelectedTip() (*externalapi.DomainHash, error) {
	return f.headersSelectedTip, nil
}

func (f *fakeOwnPruningPoint) IsInSelectedParentChainOf(_, _ *externalapi.DomainHash) (bool, error) {
	return f.onChain, nil
}

// TestServingRefusesOwnPruningPointOffHeadersChain pins the serving side: a node whose own pruning
// point is not on its headers selected tip's chain refuses to hand out the proof, without asking
// for the requester to be banned; a consistent node serves it.
func TestServingRefusesOwnPruningPointOffHeadersChain(t *testing.T) {
	pruningPoint := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{1})
	tip := externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{2})

	err := checkOwnPruningPointMeetsHeadersChain(&fakeOwnPruningPoint{pruningPoint, tip, true})
	if err != nil {
		t.Fatalf("a consistent node refused to serve: %v", err)
	}

	err = checkOwnPruningPointMeetsHeadersChain(&fakeOwnPruningPoint{pruningPoint, tip, false})
	protocolErr := &protocolerrors.ProtocolError{}
	if !errors.As(err, protocolErr) {
		t.Fatalf("expected a protocol error, got: %v", err)
	}
	if protocolErr.ShouldBan {
		t.Fatalf("refusing to serve must not ban the requester")
	}
}
