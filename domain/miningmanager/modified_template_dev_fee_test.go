package miningmanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/merkle"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/multiset"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/transactionhelper"
	"github.com/HoosatNetwork/HTND/domain/consensusreference"
	"github.com/HoosatNetwork/HTND/domain/miningmanager"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/util/staging"
)

// TestModifiedTemplateDevFeeIsValidOnAnotherNode is the two-node reproduction of the coinbase
// disagreement seen on mainnet on 2026-09-24 (block 7a7bd831..., "Output 5 script differs").
//
// A mining node caches its block template and, when a miner asks for a template with different
// coinbase data (another pay address, or the same address with other extra data, as a stratum bridge
// sends), patches the cached one with ModifyBlockTemplate instead of building a new one. On master
// (2e085c087) that patch overwrote the script of the coinbase's LAST output with the new pay address
// whenever the merge set had a red block - correct for version 1, where the last output is the red
// blocks' reward paid to this block's miner, and wrong from version 2, where every merge-set block is
// paid on its own (miner output, then dev-fee output) and the last output is the dev fee of whichever
// block sorts last. The block then paid that dev fee to the miner.
//
// The mining node does not notice: it, like most of mainnet, runs on an offset UTXO baseline, where
// verifyUTXO tolerates coinbase mismatches, so its own block is Valid. A node whose pruning point
// UTXO set matches its header is strict, recomputes the coinbase, finds the dev-fee output paid to
// the wrong script, and disqualifies the block - and, by inheritance, the chain built on it.
//
// The test builds the template on a mining node with a red block in its merge set, asks for it with
// a second pay address, and validates the result on a separate, strict node. It also replays
// master's patch on the same template and shows the split: disqualified on the strict node, valid
// on an offset-baseline node.
func TestModifiedTemplateDevFeeIsValidOnAnotherNode(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		defer constants.ForceSetBlockVersion(1)

		// Every block is version 2 (per-merge-set-block outputs with a dev fee), and K is 0 there, so
		// a block merging two parallel blocks colors one of them red.
		consensusConfig.POWScores = []uint64{1}
		consensusConfig.K = append([]externalapi.KType(nil), consensusConfig.K...)
		consensusConfig.K[1] = 0
		consensusConfig.BlockCoinbaseMaturity = 0

		factory := consensus.NewFactory()
		newNode := func(name string) testapi.TestConsensus {
			tc, teardown, err := factory.NewTestConsensus(consensusConfig, name)
			if err != nil {
				t.Fatalf("NewTestConsensus %s: %+v", name, err)
			}
			t.Cleanup(func() { teardown(false) })
			return tc
		}
		miner := newNode("TestModifiedTemplateDevFee_miner")
		strict := newNode("TestModifiedTemplateDevFee_strict")
		offset := newNode("TestModifiedTemplateDevFee_offset")
		// A second strict node judges master's patched block on its own: on the first one it would be
		// a sibling of the template already accepted there and, losing the selected-tip tie on hash,
		// would stay UTXOPendingVerification without ever being checked.
		strictAgain := newNode("TestModifiedTemplateDevFee_strictAgain")

		addBlock := func(parents ...*externalapi.DomainHash) *externalapi.DomainHash {
			hash, _, err := miner.AddBlock(parents, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			block, _, err := miner.GetBlock(hash)
			if err != nil {
				t.Fatalf("GetBlock: %+v", err)
			}
			for _, node := range []testapi.TestConsensus{strict, strictAgain, offset} {
				if err := node.ValidateAndInsertBlock(block, true, true); err != nil {
					t.Fatalf("relaying %s: %+v", hash, err)
				}
			}
			return hash
		}
		base := addBlock(consensusConfig.GenesisHash)
		addBlock(base)
		addBlock(base)

		// The offset node's pruning point UTXO set does not match its header: the state
		// pruningPointBaselineIsOffset reports, and the one in which verifyUTXO tolerates.
		stagingArea := model.NewStagingArea()
		if err := offset.PruningStore().StagePruningPoint(offset.DatabaseContext(), stagingArea, base); err != nil {
			t.Fatalf("StagePruningPoint: %+v", err)
		}
		wrong := multiset.New()
		wrong.Add([]byte("a coin the network's set does not hold"))
		offset.MultisetStore().Stage(stagingArea, base, wrong)
		if err := staging.CommitAllChanges(offset.DatabaseContext(), stagingArea); err != nil {
			t.Fatalf("CommitAllChanges: %+v", err)
		}

		tcAsConsensus := miner.(externalapi.Consensus)
		tcAsConsensusPointer := &tcAsConsensus
		miningManager := miningmanager.NewFactory().NewMiningManager(
			consensusreference.NewConsensusReference(&tcAsConsensusPointer), &consensusConfig.Params,
			mempool.DefaultConfig(&consensusConfig.Params))

		firstMiner, err := generateNewCoinbase(consensusConfig.Params.Prefix, opUsual)
		if err != nil {
			t.Fatalf("generateNewCoinbase: %v", err)
		}
		pool, err := generateNewCoinbase(consensusConfig.Params.Prefix, opUsual)
		if err != nil {
			t.Fatalf("generateNewCoinbase: %v", err)
		}
		if _, _, err := miningManager.GetBlockTemplate(firstMiner); err != nil {
			t.Fatalf("GetBlockTemplate: %v", err)
		}
		// Within the cache window, so this one is the cached template patched for the pool.
		modified, _, err := miningManager.GetBlockTemplate(pool)
		if err != nil {
			t.Fatalf("GetBlockTemplate: %v", err)
		}
		if modified.Header.Version() < 2 {
			t.Fatalf("setup: expected a version-2 template, got version %d", modified.Header.Version())
		}
		ghostdagData, err := miner.GHOSTDAGDataStore().Get(miner.DatabaseContext(), model.NewStagingArea(),
			model.VirtualBlockHash, false)
		if err != nil {
			t.Fatalf("virtual GHOSTDAG data: %+v", err)
		}
		if len(ghostdagData.MergeSetReds()) == 0 {
			t.Fatalf("setup: the template must merge a red block, which is when the patch touched outputs")
		}

		// Master's patch, replayed on a copy of the same template: the last coinbase output's script
		// is replaced with the pool's.
		masterPatched := modified.Clone()
		coinbase := masterPatched.Transactions[transactionhelper.CoinbaseTransactionIndex]
		coinbase.Outputs[len(coinbase.Outputs)-1].ScriptPublicKey = pool.ScriptPublicKey
		header := masterPatched.Header.ToMutable()
		header.SetHashMerkleRoot(merkle.CalculateHashMerkleRoot(masterPatched.Transactions))
		masterPatched.Header = header.ToImmutable()

		statusOn := func(node testapi.TestConsensus, block *externalapi.DomainBlock) externalapi.BlockStatus {
			t.Helper()
			if err := node.ValidateAndInsertBlock(block, true, true); err != nil {
				t.Fatalf("ValidateAndInsertBlock: %+v", err)
			}
			info, err := node.GetBlockInfo(consensusHash(block))
			if err != nil {
				t.Fatalf("GetBlockInfo: %+v", err)
			}
			return info.BlockStatus
		}

		// The regression: the template the mining manager hands the pool is valid on another node.
		// With master's ModifyBlockTemplate this block IS the master-patched one below.
		if status := statusOn(strict, modified); status != externalapi.StatusUTXOValid {
			t.Fatalf("a template patched for another pay address was %s on a strict node - the patch "+
				"changed a coinbase output that does not depend on the pay address", status)
		}

		// The divergence as it happened: the same block, two honest nodes, two verdicts.
		if status := statusOn(offset, masterPatched); status != externalapi.StatusUTXOValid {
			t.Fatalf("master-patched block on the offset-baseline node: expected %s (coinbase mismatch "+
				"tolerated), got %s", externalapi.StatusUTXOValid, status)
		}
		if status := statusOn(strictAgain, masterPatched); status != externalapi.StatusDisqualifiedFromChain {
			t.Fatalf("master-patched block on the strict node: expected %s, got %s",
				externalapi.StatusDisqualifiedFromChain, status)
		}
	})
}

func consensusHash(block *externalapi.DomainBlock) *externalapi.DomainHash {
	return consensushashing.BlockHash(block)
}
