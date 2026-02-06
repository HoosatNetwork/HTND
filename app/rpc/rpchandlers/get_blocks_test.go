package rpchandlers_test

import (
	"reflect"
	"testing"

	"github.com/Hoosat-Oy/HTND/app/appmessage"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpccontext"
	"github.com/Hoosat-Oy/HTND/app/rpc/rpchandlers"
	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/testapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/hashset"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
	"github.com/Hoosat-Oy/HTND/domain/miningmanager"
	"github.com/Hoosat-Oy/HTND/infrastructure/config"
)

type fakeDomain struct {
	testapi.TestConsensus
}

func (d fakeDomain) ConsensusEventsChannel() chan externalapi.ConsensusEvent {
	panic("implement me")
}

func (d fakeDomain) DeleteStagingConsensus() error {
	panic("implement me")
}

func (d fakeDomain) StagingConsensus() externalapi.Consensus {
	panic("implement me")
}

func (d fakeDomain) InitStagingConsensusWithoutGenesis() error {
	panic("implement me")
}

func (d fakeDomain) CommitStagingConsensus() error {
	panic("implement me")
}

func (d fakeDomain) Consensus() externalapi.Consensus           { return d }
func (d fakeDomain) MiningManager() miningmanager.MiningManager { return nil }

func TestHandleGetBlocks(t *testing.T) {
	// Note: We only test on testnet because HandleGetBlocks returns empty response when node is not nearly synced,
	// which is always the case in test environment for mainnet (old genesis timestamp).
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		// Skip mainnet due to the "not nearly synced" check in the RPC handler
		if consensusConfig.Name == "hoosat-mainnet" {
			t.Skip("Skipping mainnet - RPC returns empty when not nearly synced")
		}
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestHandleGetBlocks")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		fakeContext := rpccontext.Context{
			Config: &config.Config{Flags: &config.Flags{NetworkFlags: config.NetworkFlags{ActiveNetParams: &consensusConfig.Params}}},
			Domain: fakeDomain{tc},
		}

		getBlocks := func(lowHash *externalapi.DomainHash) *appmessage.GetBlocksResponseMessage {
			request := appmessage.GetBlocksRequestMessage{}
			if lowHash != nil {
				request.LowHash = lowHash.String()
			}
			response, err := rpchandlers.HandleGetBlocks(&fakeContext, nil, &request)
			if err != nil {
				t.Fatalf("Expected empty request to not fail, instead: '%v'", err)
			}
			return response.(*appmessage.GetBlocksResponseMessage)
		}

		// Helper to convert string slice to hashset
		toHashSet := func(hashes []string) hashset.HashSet {
			set := hashset.New()
			for _, h := range hashes {
				hash, err := externalapi.NewDomainHashFromString(h)
				if err != nil {
					t.Fatalf("Failed to parse hash: %v", err)
				}
				set.Add(hash)
			}
			return set
		}

		// Create a DAG with the following structure:
		//          merging block
		//         /      |      \
		//      split1  split2   split3
		//        \       |      /
		//         merging block
		//         /      |      \
		//      split1  split2   split3
		//        \       |      /
		//               etc.
		allBlocks := hashset.New()
		allBlocks.Add(consensusConfig.GenesisHash)
		mergingBlock := consensusConfig.GenesisHash
		for i := 0; i < 10; i++ {
			splitBlocks := make([]*externalapi.DomainHash, 0, 3)
			for j := 0; j < 3; j++ {
				blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{mergingBlock}, nil, nil)
				if err != nil {
					t.Fatalf("Failed adding block: %v", err)
				}
				splitBlocks = append(splitBlocks, blockHash)
				allBlocks.Add(blockHash)
			}

			mergingBlock, _, err = tc.AddBlock(splitBlocks, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			allBlocks.Add(mergingBlock)
		}

		virtualSelectedParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("Failed getting SelectedParent: %v", err)
		}

		// Test: requesting with virtualSelectedParent as lowHash should return just that block
		requestSelectedParent := getBlocks(virtualSelectedParent)
		if len(requestSelectedParent.BlockHashes) != 1 || requestSelectedParent.BlockHashes[0] != virtualSelectedParent.String() {
			t.Fatalf("TestHandleGetBlocks expected just %s, got: %v", virtualSelectedParent, requestSelectedParent.BlockHashes)
		}

		// Test: requesting all blocks (lowHash=nil) should return all blocks in the DAG
		actualOrder := getBlocks(nil)
		actualSet := toHashSet(actualOrder.BlockHashes)
		if actualSet.Length() != allBlocks.Length() {
			t.Fatalf("TestHandleGetBlocks expected %d blocks, got %d", allBlocks.Length(), actualSet.Length())
		}
		for _, blockHash := range allBlocks.ToSlice() {
			if !actualSet.Contains(blockHash) {
				t.Fatalf("TestHandleGetBlocks: block %s missing from result", blockHash)
			}
		}

		// Test: requesting from genesis should return all blocks
		requestAllExplicitly := getBlocks(consensusConfig.GenesisHash)
		actualSetExplicit := toHashSet(requestAllExplicitly.BlockHashes)
		if actualSetExplicit.Length() != allBlocks.Length() {
			t.Fatalf("TestHandleGetBlocks expected %d blocks from genesis, got %d", allBlocks.Length(), actualSetExplicit.Length())
		}

		// Verify that when lowHash==highHash we get a slice with a single hash
		actualBlocks := getBlocks(virtualSelectedParent)
		if !reflect.DeepEqual(actualBlocks.BlockHashes, []string{virtualSelectedParent.String()}) {
			t.Fatalf("TestHandleGetBlocks expected blocks to contain just '%s', instead got: \n%v",
				virtualSelectedParent, actualBlocks.BlockHashes)
		}
	})
}
