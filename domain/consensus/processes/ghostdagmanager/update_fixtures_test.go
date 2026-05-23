package ghostdagmanager_test

import (
	"bytes"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/processes/ghostdagmanager"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/blockheader"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/constants"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/testutils"
)

func domainHashToFixtureID(hash *externalapi.DomainHash) string {
	b := hash.ByteSlice()
	b = bytes.TrimRight(b, "\x00")
	return string(b)
}

func domainHashesToFixtureIDs(arr []*externalapi.DomainHash) []string {
	out := make([]string, len(arr))
	for i, h := range arr {
		out[i] = domainHashToFixtureID(h)
	}
	return out
}

// TestUpdateGHOSTDAGFixtures rewrites the GhostDAG golden JSONs to match the current implementation.
//
// Usage:
//
//	UPDATE_GHOSTDAG_FIXTURES=1 go test ./domain/consensus/processes/ghostdagmanager -run TestUpdateGHOSTDAGFixtures -count=1
func TestUpdateGHOSTDAGFixtures(t *testing.T) {
	if os.Getenv("UPDATE_GHOSTDAG_FIXTURES") != "1" {
		t.Skip("set UPDATE_GHOSTDAG_FIXTURES=1 to update fixtures")
	}

	implementationFactories := []implManager{
		{ghostdagmanager.New, "Original"},
	}

	// Run once using mainnet; these fixtures are net-agnostic (they use synthetic hashes).
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		if consensusConfig.Name != "hoosat-mainnet" {
			return
		}

		genesisHeader := consensusConfig.GenesisBlock.Header
		fixturesRoot := filepath.Join("..", "..", "testdata", "dags")
		paths, err := filepath.Glob(filepath.Join(fixturesRoot, "*.json"))
		if err != nil {
			t.Fatalf("glob fixtures: %v", err)
		}
		if len(paths) == 0 {
			t.Fatalf("no fixtures found under %s", fixturesRoot)
		}

		for _, path := range paths {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}

			var test testDag
			decoder := json.NewDecoder(bytes.NewReader(b))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&test); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}

			// Match TestGHOSTDAG: set K for the currently active block version.
			consensusConfig.K[constants.GetBlockVersion()-1] = test.K

			genesisHash := *StringToDomainHash(test.GenesisID)

			for _, impl := range implementationFactories {
				dagTopology := &DAGTopologyManagerImpl{
					parentsMap:  make(map[externalapi.DomainHash][]*externalapi.DomainHash),
					childrenMap: make(map[externalapi.DomainHash][]*externalapi.DomainHash),
				}

				ghostdagDataStore := &GHOSTDAGDataStoreImpl{
					dagMap: make(map[externalapi.DomainHash]*externalapi.BlockGHOSTDAGData),
				}

				blockHeadersStore := &blockHeadersStore{
					dagMap: make(map[externalapi.DomainHash]externalapi.BlockHeader),
				}

				blockGHOSTDAGDataGenesis := externalapi.NewBlockGHOSTDAGData(0, new(big.Int), nil, nil, nil, nil,
					externalapi.KType(1))
				dagTopology.parentsMap[genesisHash] = nil
				ghostdagDataStore.dagMap[genesisHash] = blockGHOSTDAGDataGenesis
				blockHeadersStore.dagMap[genesisHash] = genesisHeader

				g := impl.function(nil, dagTopology, nil, ghostdagDataStore, blockHeadersStore, nil, []externalapi.KType{test.K}, &genesisHash)

				for i := range test.Blocks {
					blk := &test.Blocks[i]

					blockID := StringToDomainHash(blk.ID)
					dagTopology.parentsMap[*blockID] = StringToDomainHashSlice(blk.Parents)
					for _, parentID := range blk.Parents {
						parentHash := StringToDomainHash(parentID)
						dagTopology.childrenMap[*parentHash] = append(dagTopology.childrenMap[*parentHash], blockID)
					}

					blockHeadersStore.dagMap[*blockID] = blockheader.NewImmutableBlockHeader(
						constants.GetBlockVersion(),
						[]externalapi.BlockLevelParents{StringToDomainHashSlice(blk.Parents)},
						nil,
						nil,
						nil,
						0,
						genesisHeader.Bits(),
						0,
						0,
						0,
						new(big.Int),
						nil,
					)

					if err := g.GHOSTDAG(nil, blockID); err != nil {
						t.Fatalf("%s: GHOSTDAG failed for %s: %v", filepath.Base(path), blk.ID, err)
					}

					ghostdagData, err := ghostdagDataStore.Get(nil, nil, blockID, false)
					if err != nil {
						t.Fatalf("%s: ghostdagDataStore Get failed for %s: %v", filepath.Base(path), blk.ID, err)
					}

					blk.Score = ghostdagData.BlueScore()
					blk.SelectedParent = domainHashToFixtureID(ghostdagData.SelectedParent())
					blk.MergeSetBlues = domainHashesToFixtureIDs(ghostdagData.MergeSetBlues())
					blk.MergeSetReds = domainHashesToFixtureIDs(ghostdagData.MergeSetReds())
				}
			}

			out, err := json.MarshalIndent(&test, "", "    ")
			if err != nil {
				t.Fatalf("marshal %s: %v", path, err)
			}
			out = append(out, '\n')

			if err := os.WriteFile(path, out, 0o644); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
	})
}
