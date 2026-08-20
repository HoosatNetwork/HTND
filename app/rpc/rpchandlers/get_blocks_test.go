package rpchandlers_test

import (
	"crypto/rand"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"

	"github.com/HoosatNetwork/HTND/app/appmessage"
	"github.com/HoosatNetwork/HTND/app/rpc/rpccontext"
	"github.com/HoosatNetwork/HTND/app/rpc/rpchandlers"
	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/testapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/hashes"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/domain/miningmanager"
	"github.com/HoosatNetwork/HTND/infrastructure/config"
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
	os.Setenv("HTND_TEST_MODE", "true")
	defer os.Unsetenv("HTND_TEST_MODE")
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		stagingArea := model.NewStagingArea()

		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestHandleGetBlocks")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		cfg := &config.Config{
			Flags: &config.Flags{},
		}
		cfg.ActiveNetParams = &consensusConfig.Params

		fakeContext := rpccontext.Context{
			Config: cfg,
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

		filterAntiPast := func(povBlock *externalapi.DomainHash, slice []*externalapi.DomainHash) []*externalapi.DomainHash {
			antipast := make([]*externalapi.DomainHash, 0, len(slice))

			for _, blockHash := range slice {
				isInPastOfPovBlock, err := tc.DAGTopologyManager().IsAncestorOf(stagingArea, blockHash, povBlock)
				if err != nil {
					t.Fatalf("Failed doing reachability check: '%v'", err)
				}
				if !isInPastOfPovBlock {
					antipast = append(antipast, blockHash)
				}
			}
			return antipast
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
		expectedOrder := make([]*externalapi.DomainHash, 0, 40)
		mergingBlock := consensusConfig.GenesisHash
		for range 10 {
			splitBlocks := make([]*externalapi.DomainHash, 0, 3)
			for range 3 {
				blockHash, _, err := tc.AddBlock([]*externalapi.DomainHash{mergingBlock}, nil, nil)
				if err != nil {
					t.Fatalf("Failed adding block: %v", err)
				}
				splitBlocks = append(splitBlocks, blockHash)
			}
			sort.Sort(sort.Reverse(testutils.NewTestGhostDAGSorter(stagingArea, splitBlocks, tc, t)))
			restOfSplitBlocks, selectedParent := splitBlocks[:len(splitBlocks)-1], splitBlocks[len(splitBlocks)-1]
			expectedOrder = append(expectedOrder, selectedParent)
			expectedOrder = append(expectedOrder, restOfSplitBlocks...)

			mergingBlock, _, err = tc.AddBlock(splitBlocks, nil, nil)
			if err != nil {
				t.Fatalf("Failed adding block: %v", err)
			}
			expectedOrder = append(expectedOrder, mergingBlock)
		}

		virtualSelectedParent, err := tc.GetVirtualSelectedParent()
		if err != nil {
			t.Fatalf("Failed getting SelectedParent: %v", err)
		}
		if !virtualSelectedParent.Equal(expectedOrder[len(expectedOrder)-1]) {
			t.Fatalf("Expected %s to be selectedParent, instead found: %s", expectedOrder[len(expectedOrder)-1], virtualSelectedParent)
		}

		requestSelectedParent := getBlocks(virtualSelectedParent)
		if !reflect.DeepEqual(requestSelectedParent.BlockHashes, hashes.ToStrings([]*externalapi.DomainHash{virtualSelectedParent})) {
			t.Fatalf("TestHandleGetBlocks expected:\n%v\nactual:\n%v", virtualSelectedParent, requestSelectedParent.BlockHashes)
		}

		for i, blockHash := range expectedOrder {
			expectedBlocks := filterAntiPast(blockHash, expectedOrder)
			expectedBlocks = append([]*externalapi.DomainHash{blockHash}, expectedBlocks...)

			actualBlocks := getBlocks(blockHash)
			expectedHashes := hashes.ToStrings(expectedBlocks)
			actualHashes := actualBlocks.BlockHashes

			// Check that the same hashes are returned (regardless of order)
			if len(expectedHashes) != len(actualHashes) {
				t.Fatalf("TestHandleGetBlocks %d length mismatch expected %d actual %d", i, len(expectedHashes), len(actualHashes))
			}

			// Create maps for set comparison
			expectedSet := make(map[string]struct{}, len(expectedHashes))
			for _, h := range expectedHashes {
				expectedSet[h] = struct{}{}
			}
			actualSet := make(map[string]struct{}, len(actualHashes))
			for _, h := range actualHashes {
				actualSet[h] = struct{}{}
			}

			if !reflect.DeepEqual(expectedSet, actualSet) {
				// Find missing and extra hashes
				missing := []string{}
				for h := range expectedSet {
					if _, exists := actualSet[h]; !exists {
						missing = append(missing, h)
					}
				}
				extra := []string{}
				for h := range actualSet {
					if _, exists := expectedSet[h]; !exists {
						extra = append(extra, h)
					}
				}
				t.Fatalf("TestHandleGetBlocks %d set mismatch. Missing: %v, Extra: %v", i, missing, extra)
			}
		}

		// Make explicitly sure that if lowHash==highHash we get a slice with a single hash.
		actualBlocks := getBlocks(virtualSelectedParent)
		if !reflect.DeepEqual(actualBlocks.BlockHashes, []string{virtualSelectedParent.String()}) {
			t.Fatalf("TestHandleGetBlocks expected blocks to contain just '%s', instead got: \n%v",
				virtualSelectedParent, actualBlocks.BlockHashes)
		}

		expectedOrder = append([]*externalapi.DomainHash{consensusConfig.GenesisHash}, expectedOrder...)
		actualOrder := getBlocks(nil)

		// Check that the same hashes are returned (regardless of order)
		actualOrderSet := make(map[string]struct{}, len(actualOrder.BlockHashes))
		for _, h := range actualOrder.BlockHashes {
			actualOrderSet[h] = struct{}{}
		}
		expectedOrderSet := make(map[string]struct{}, len(expectedOrder))
		for _, h := range expectedOrder {
			expectedOrderSet[h.String()] = struct{}{}
		}
		if !reflect.DeepEqual(actualOrderSet, expectedOrderSet) {
			// Find missing and extra hashes
			missing := []string{}
			for h := range expectedOrderSet {
				if _, exists := actualOrderSet[h]; !exists {
					missing = append(missing, h)
				}
			}
			extra := []string{}
			for h := range actualOrderSet {
				if _, exists := expectedOrderSet[h]; !exists {
					extra = append(extra, h)
				}
			}
			t.Fatalf("TestHandleGetBlocks set mismatch when getting all blocks. Missing: %v, Extra: %v", missing, extra)
		}

		requestAllExplictly := getBlocks(consensusConfig.GenesisHash)
		requestAllSet := make(map[string]struct{}, len(requestAllExplictly.BlockHashes))
		for _, h := range requestAllExplictly.BlockHashes {
			requestAllSet[h] = struct{}{}
		}
		if !reflect.DeepEqual(requestAllSet, expectedOrderSet) {
			// Find missing and extra hashes
			missing := []string{}
			for h := range expectedOrderSet {
				if _, exists := requestAllSet[h]; !exists {
					missing = append(missing, h)
				}
			}
			extra := []string{}
			for h := range requestAllSet {
				if _, exists := expectedOrderSet[h]; !exists {
					extra = append(extra, h)
				}
			}
			t.Fatalf("TestHandleGetBlocks set mismatch when requesting all explicitly. Missing: %v, Extra: %v", missing, extra)
		}
	})
}

func TestHandleGetBlocksCacheRespectsIncludeFlags(t *testing.T) {
	os.Setenv("HTND_TEST_MODE", "true")
	defer os.Unsetenv("HTND_TEST_MODE")

	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, teardown, err := factory.NewTestConsensus(consensusConfig, "TestHandleGetBlocksCacheRespectsIncludeFlags")
		if err != nil {
			t.Fatalf("Error setting up consensus: %+v", err)
		}
		defer teardown(false)

		cfg := &config.Config{
			Flags: &config.Flags{},
		}
		cfg.ActiveNetParams = &consensusConfig.Params

		fakeContext := rpccontext.Context{
			Config: cfg,
			Domain: fakeDomain{tc},
		}

		extra := make([]byte, 32)
		_, _ = rand.Read(extra)
		coinbaseData := &externalapi.DomainCoinbaseData{
			ScriptPublicKey: &externalapi.ScriptPublicKey{Script: []byte{0x51}, Version: 0},
			ExtraData:       extra,
		}
		lowHash, _, err := tc.AddBlock([]*externalapi.DomainHash{consensusConfig.GenesisHash}, coinbaseData, nil)
		if err != nil {
			t.Fatalf("Failed adding block: %v", err)
		}

		// 1) Warm cache with IncludeBlocks=false.
		requestNoBlocks := &appmessage.GetBlocksRequestMessage{LowHash: lowHash.String(), IncludeBlocks: false, IncludeTransactions: false}
		respNoBlocksMsg, err := rpchandlers.HandleGetBlocks(&fakeContext, nil, requestNoBlocks)
		if err != nil {
			t.Fatalf("HandleGetBlocks returned error: %v", err)
		}
		respNoBlocks := respNoBlocksMsg.(*appmessage.GetBlocksResponseMessage)
		if respNoBlocks.Error != nil {
			t.Fatalf("HandleGetBlocks returned RPC error: %v", respNoBlocks.Error)
		}
		if respNoBlocks.Blocks != nil {
			t.Fatalf("expected Blocks to be nil when IncludeBlocks=false")
		}
		if len(respNoBlocks.BlockHashes) == 0 {
			t.Fatalf("expected non-empty BlockHashes")
		}

		// 2) Immediately request with IncludeBlocks=true. Old buggy cache keying would return Blocks=nil here.
		requestWithBlocks := &appmessage.GetBlocksRequestMessage{LowHash: lowHash.String(), IncludeBlocks: true, IncludeTransactions: false}
		respWithBlocksMsg, err := rpchandlers.HandleGetBlocks(&fakeContext, nil, requestWithBlocks)
		if err != nil {
			t.Fatalf("HandleGetBlocks returned error: %v", err)
		}
		respWithBlocks := respWithBlocksMsg.(*appmessage.GetBlocksResponseMessage)
		if respWithBlocks.Error != nil {
			t.Fatalf("HandleGetBlocks returned RPC error: %v", respWithBlocks.Error)
		}
		if respWithBlocks.Blocks == nil {
			t.Fatalf("expected Blocks to be populated when IncludeBlocks=true")
		}
		if len(respWithBlocks.Blocks) != len(respWithBlocks.BlockHashes) {
			t.Fatalf("expected Blocks length %d, got %d", len(respWithBlocks.BlockHashes), len(respWithBlocks.Blocks))
		}

		for i, hashString := range respWithBlocks.BlockHashes {
			block := respWithBlocks.Blocks[i]
			if block.VerboseData == nil {
				t.Fatalf("expected VerboseData to be populated for block %s", hashString)
			}
			if block.VerboseData.Hash != hashString {
				t.Fatalf("expected VerboseData.Hash to equal %s, got %s", hashString, block.VerboseData.Hash)
			}

			hash, err := externalapi.NewDomainHashFromString(hashString)
			if err != nil {
				t.Fatalf("failed parsing hash %s: %v", hashString, err)
			}
			info, err := tc.GetBlockInfo(hash)
			if err != nil {
				t.Fatalf("failed getting block info for %s: %v", hashString, err)
			}

			if !reflect.DeepEqual(block.VerboseData.MergeSetBluesHashes, hashes.ToStrings(info.MergeSetBlues)) {
				t.Fatalf("MergeSetBluesHashes mismatch for %s", hashString)
			}
			if !reflect.DeepEqual(block.VerboseData.MergeSetRedsHashes, hashes.ToStrings(info.MergeSetReds)) {
				t.Fatalf("MergeSetRedsHashes mismatch for %s", hashString)
			}
		}
	})
}
