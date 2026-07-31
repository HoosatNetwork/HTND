package headersselectedtipmanager_test

import (
	"testing"

	"github.com/HoosatNetwork/HTND/domain/consensus/model"

	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/pkg/errors"
)

func TestAddHeaderTip(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		factory := consensus.NewFactory()
		tc, tearDown, err := factory.NewTestConsensus(consensusConfig, "TestAddHeaderTip")
		if err != nil {
			t.Fatalf("NewTestConsensus: %s", err)
		}
		defer tearDown(false)

		stagingArea := model.NewStagingArea()
		checkExpectedSelectedChain := func(expectedSelectedChain []*externalapi.DomainHash) {
			for i, blockHash := range expectedSelectedChain {
				if i < 0 {
					t.Fatalf("negative chain index %d", i)
				}
				chainIndex := uint64(uint(i))
				chainBlockHash, err := tc.HeadersSelectedChainStore().GetHashByIndex(tc.DatabaseContext(), stagingArea, chainIndex)
				if err != nil {
					t.Fatalf("GetHashByIndex: %+v", err)
				}

				if !blockHash.Equal(chainBlockHash) {
					t.Fatalf("chain block %d is expected to be %s but got %s", i, blockHash, chainBlockHash)
				}

				index, err := tc.HeadersSelectedChainStore().GetIndexByHash(tc.DatabaseContext(), stagingArea, blockHash)
				if err != nil {
					t.Fatalf("GetIndexByHash: %+v", err)
				}

				if chainIndex != index {
					t.Fatalf("chain block %s is expected to be %d but got %d", blockHash, i, index)
				}
			}

			nextExpectedIndexInt := len(expectedSelectedChain) + 1
			if nextExpectedIndexInt < 0 {
				t.Fatalf("negative next chain index %d", nextExpectedIndexInt)
			}
			nextExpectedIndex := uint64(uint(nextExpectedIndexInt))
			_, err := tc.HeadersSelectedChainStore().GetHashByIndex(tc.DatabaseContext(), stagingArea, nextExpectedIndex)
			if !errors.Is(err, database.ErrNotFound) {
				t.Fatalf("index %d is not expected to exist, but instead got error: %+v",
					nextExpectedIndex, err)
			}
		}

		expectedSelectedChain := []*externalapi.DomainHash{consensusConfig.GenesisHash}
		tipHash := consensusConfig.GenesisHash
		for range 10 {
			var err error
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}

			expectedSelectedChain = append(expectedSelectedChain, tipHash)
			checkExpectedSelectedChain(expectedSelectedChain)
		}

		expectedSelectedChain = []*externalapi.DomainHash{consensusConfig.GenesisHash}
		tipHash = consensusConfig.GenesisHash
		for range 11 {
			var err error
			tipHash, _, err = tc.AddBlock([]*externalapi.DomainHash{tipHash}, nil, nil)
			if err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}

			expectedSelectedChain = append(expectedSelectedChain, tipHash)
		}
		checkExpectedSelectedChain(expectedSelectedChain)
	})
}
