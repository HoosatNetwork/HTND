package main

import (
	"github.com/Hoosat-Oy/HTND/domain/consensus"
	"github.com/Hoosat-Oy/HTND/domain/consensus/model/externalapi"
	"github.com/Hoosat-Oy/HTND/domain/consensus/utils/consensushashing"
	"github.com/Hoosat-Oy/HTND/infrastructure/db/database/pebble"
	"github.com/Hoosat-Oy/HTND/stability-tests/common"
	"github.com/Hoosat-Oy/HTND/stability-tests/common/mine"
	"github.com/pkg/errors"
)

const leveldbCacheSizeMiB = 256

func prepareBlocks() (blocks []*externalapi.DomainBlock, topBlock *externalapi.DomainBlock, err error) {
	config := activeConfig()
	testDatabaseDir, err := common.TempDir("minejson")
	if err != nil {
		return nil, nil, err
	}
	db, err := pebble.NewPebbleDB(testDatabaseDir, leveldbCacheSizeMiB)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()

	testConsensus, tearDownFunc, err := consensus.NewFactory().NewTestConsensus(&consensus.Config{Params: *config.ActiveNetParams}, "prepareBlocks")
	if err != nil {
		return nil, nil, err
	}
	defer tearDownFunc(true)

	virtualSelectedParent, err := testConsensus.GetVirtualSelectedParent()
	if err != nil {
		return nil, nil, err
	}
	currentParentHash := virtualSelectedParent

	blocksCount := config.OrphanChainLength + 1
	blocks = make([]*externalapi.DomainBlock, 0, blocksCount)

	for range blocksCount {
		block, _, err := testConsensus.BuildBlockWithParents(
			[]*externalapi.DomainHash{currentParentHash},
			&externalapi.DomainCoinbaseData{ScriptPublicKey: &externalapi.ScriptPublicKey{}},
			[]*externalapi.DomainTransaction{})
		if err != nil {
			return nil, nil, errors.Wrap(err, "error in BuildBlockWithParents")
		}

		mine.SolveBlock(block)
		err = testConsensus.ValidateAndInsertBlock(block, true, true)
		if err != nil {
			return nil, nil, errors.Wrap(err, "error in ValidateAndInsertBlock")
		}

		blocks = append(blocks, block)
		currentParentHash = consensushashing.BlockHash(block)
	}

	return blocks[:len(blocks)-1], blocks[len(blocks)-1], nil
}
