package domain_test

import (
	"errors"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/HoosatNetwork/HTND/v2/domain"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/consensushashing"
	"github.com/HoosatNetwork/HTND/v2/domain/consensus/utils/testutils"
	"github.com/HoosatNetwork/HTND/v2/domain/miningmanager/mempool"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/v2/infrastructure/db/database/ldb"
)

// failingCompactDatabase fails compaction on demand, which is the last step of deleting the inactive
// prefix after a staging consensus commit.
type failingCompactDatabase struct {
	database.Database
	fail bool
}

func (db *failingCompactDatabase) Compact() error {
	if db.fail {
		return errors.New("compaction failed")
	}
	return db.Database.Compact()
}

// TestCommitStagingConsensusSwapsEvenIfCleanupFails pins that once the prefix swap is committed, the
// domain serves the committed consensus even if deleting the old prefix's data fails. The swap used to
// happen after that cleanup, so a cleanup failure left the node running on the old consensus instance,
// whose prefix the database already marked inactive.
func TestCommitStagingConsensusSwapsEvenIfCleanupFails(t *testing.T) {
	testutils.ForAllNets(t, true, func(t *testing.T, consensusConfig *consensus.Config) {
		dataDir, err := os.MkdirTemp("", fmt.Sprintf("TestCommitStagingConsensusSwapsEvenIfCleanupFails-%s", consensusConfig.Name))
		if err != nil {
			t.Fatalf("os.MkdirTemp: %+v", err)
		}
		defer os.RemoveAll(dataDir)

		levelDB, err := ldb.NewLevelDB(dataDir, 8)
		if err != nil {
			t.Fatalf("NewLevelDB: %+v", err)
		}
		db := &failingCompactDatabase{Database: levelDB}

		domainInstance, err := domain.New(consensusConfig, mempool.DefaultConfig(&consensusConfig.Params), db)
		if err != nil {
			t.Fatalf("New: %+v", err)
		}
		if err := domainInstance.InitStagingConsensusWithoutGenesis(); err != nil {
			t.Fatalf("InitStagingConsensusWithoutGenesis: %+v", err)
		}

		genesisWithTrustedData := &externalapi.BlockWithTrustedData{
			Block: consensusConfig.GenesisBlock,
			GHOSTDAGData: []*externalapi.BlockGHOSTDAGDataHashPair{{
				GHOSTDAGData: externalapi.NewBlockGHOSTDAGData(0, big.NewInt(0), model.VirtualGenesisBlockHash, nil, nil,
					make(map[externalapi.DomainHash]externalapi.KType), externalapi.KType(1)),
				Hash: consensusConfig.GenesisHash,
			}},
		}
		if err := domainInstance.StagingConsensus().ValidateAndInsertBlockWithTrustedData(genesisWithTrustedData, true); err != nil {
			t.Fatalf("ValidateAndInsertBlockWithTrustedData: %+v", err)
		}
		block, err := domainInstance.StagingConsensus().BuildBlock(&externalapi.DomainCoinbaseData{
			ScriptPublicKey: &externalapi.ScriptPublicKey{}, ExtraData: []byte{},
		}, nil)
		if err != nil {
			t.Fatalf("BuildBlock: %+v", err)
		}
		if err := domainInstance.StagingConsensus().ValidateAndInsertBlock(block, true, false); err != nil {
			t.Fatalf("ValidateAndInsertBlock: %+v", err)
		}
		blockHash := consensushashing.BlockHash(block)

		db.fail = true
		commitErr := domainInstance.CommitStagingConsensus()
		db.fail = false

		blockInfo, err := domainInstance.Consensus().GetBlockInfo(blockHash)
		if err != nil {
			t.Fatalf("GetBlockInfo: %+v", err)
		}
		if !blockInfo.Exists {
			t.Fatalf("after the prefix swap was committed the domain still serves the old consensus (commit error: %v)", commitErr)
		}
		if domainInstance.StagingConsensus() != nil {
			t.Fatalf("the committed staging consensus should no longer be held as staging")
		}
		if commitErr != nil {
			t.Fatalf("a failed cleanup of the old prefix should not fail the commit: %+v", commitErr)
		}
	})
}
