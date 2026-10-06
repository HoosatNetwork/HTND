package pruningproofmanager

import (
	"math"
	"math/big"
	"testing"

	consensusdatabase "github.com/HoosatNetwork/HTND/v2/domain/consensus/database"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/blockheader"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/constants"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
)

// TestProofValidationColorsHeadersByTheirOwnVersion pins that the GHOSTDAG managers pruning proof validation builds
// color each proof header by the rules of its own version. They read the process-global block version instead - 1
// after a restart, the tip version on a node that has been running - while applying the same proof colors each header
// by its own version, so the proof a node accepted and the proof it applied were colored under different rules.
func TestProofValidationColorsHeadersByTheirOwnVersion(t *testing.T) {
	defer constants.ForceSetBlockVersion(1)

	db, err := ldb.NewLevelDB(t.TempDir(), 8)
	if err != nil {
		t.Fatalf("NewLevelDB: %+v", err)
	}
	defer db.Close()

	// Every header is version 1, where K is 0: a block merging two parallel blocks colors the side one red.
	ppm := &pruningProofManager{
		databaseContext: consensusdatabase.New(db),
		k:               []externalapi.KType{0, 40, 40, 40, 40, 40, 40, 40, 40},
		genesisHash:     externalapi.NewDomainHashFromByteArray(&[externalapi.DomainHashSize]byte{0xff}),
		powScores:       []uint64{math.MaxUint64},
	}

	colorMerge := func(globalVersion uint) *externalapi.BlockGHOSTDAGData {
		constants.ForceSetBlockVersion(globalVersion)
		defer constants.ForceSetBlockVersion(1)

		// As ValidatePruningPointProof builds and seeds its level-0 DAG.
		headerStore, relationStores, reachabilityStores, ghostdagStores, err := ppm.dagStores(0)
		if err != nil {
			t.Fatalf("dagStores: %+v", err)
		}
		reachabilityManagers, topologyManagers, ghostdagManagers, _ := ppm.dagProcesses(0, headerStore, relationStores,
			reachabilityStores, ghostdagStores)
		stagingArea := model.NewStagingArea()
		if err := reachabilityManagers[0].Init(stagingArea); err != nil {
			t.Fatalf("Init: %+v", err)
		}
		if err := topologyManagers[0].SetParents(stagingArea, model.VirtualGenesisBlockHash, nil); err != nil {
			t.Fatalf("SetParents: %+v", err)
		}
		ghostdagStores[0].Stage(stagingArea, model.VirtualGenesisBlockHash,
			externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), nil, nil, nil, nil, 1), false)

		add := func(nonce, daaScore uint64, parents ...*externalapi.DomainHash) *externalapi.DomainHash {
			header := blockheader.NewImmutableBlockHeader(1, []externalapi.BlockLevelParents{parents},
				&externalapi.DomainHash{}, &externalapi.DomainHash{}, &externalapi.DomainHash{}, 0, 0x207fffff, nonce,
				daaScore, 0, big.NewInt(0), &externalapi.DomainHash{})
			hash := consensushashing.HeaderHash(header)
			headerStore.Stage(stagingArea, hash, header)
			if err := topologyManagers[0].SetParents(stagingArea, hash, parents); err != nil {
				t.Fatalf("SetParents: %+v", err)
			}
			if err := ghostdagManagers[0].GHOSTDAG(stagingArea, hash); err != nil {
				t.Fatalf("GHOSTDAG: %+v", err)
			}
			if err := reachabilityManagers[0].AddBlock(stagingArea, hash); err != nil {
				t.Fatalf("AddBlock: %+v", err)
			}
			return hash
		}
		base := add(1, 10, model.VirtualGenesisBlockHash)
		left := add(2, 11, base)
		right := add(3, 11, base)
		merge := add(4, 12, left, right)

		data, err := ghostdagStores[0].Get(ppm.databaseContext, stagingArea, merge, false)
		if err != nil {
			t.Fatalf("Get: %+v", err)
		}
		return data
	}

	atOne, atNine := colorMerge(1), colorMerge(9)
	if len(atOne.MergeSetReds()) != 1 {
		t.Fatalf("setup: with K=0 the merge header should have one red, got %d blues and %d reds",
			len(atOne.MergeSetBlues()), len(atOne.MergeSetReds()))
	}
	if len(atNine.MergeSetBlues()) != len(atOne.MergeSetBlues()) || len(atNine.MergeSetReds()) != len(atOne.MergeSetReds()) {
		t.Fatalf("proof validation colored the same version-1 header with %d blues/%d reds at global 9 and %d/%d at 1",
			len(atNine.MergeSetBlues()), len(atNine.MergeSetReds()), len(atOne.MergeSetBlues()), len(atOne.MergeSetReds()))
	}
}
