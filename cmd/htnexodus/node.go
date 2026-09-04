package main

import (
	"strings"

	"github.com/HoosatNetwork/HTND/domain"
	"github.com/HoosatNetwork/HTND/domain/consensus"
	"github.com/HoosatNetwork/HTND/domain/consensus/model/externalapi"
	"github.com/HoosatNetwork/HTND/domain/dagconfig"
	"github.com/HoosatNetwork/HTND/domain/miningmanager/mempool"
	infrastructuredatabase "github.com/HoosatNetwork/HTND/infrastructure/db/database"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/ldb"
	"github.com/HoosatNetwork/HTND/infrastructure/db/database/pebble"
	"github.com/pkg/errors"
)

const (
	leveldbCacheSizeMiB  = 256
	pebbledbCacheSizeMiB = 2048
)

func netParamsByName(network string) (*dagconfig.Params, error) {
	switch strings.ToLower(network) {
	case "", "mainnet", "hoosat-mainnet":
		return &dagconfig.MainnetParams, nil
	case "testnet", "hoosat-testnet":
		return &dagconfig.TestnetParams, nil
	case "testnet-b5", "hoosat-testnet-b5":
		return &dagconfig.TestnetParamsB5, nil
	case "testnet-b10", "hoosat-testnet-b10":
		return &dagconfig.TestnetParamsB10, nil
	case "simnet", "hoosat-simnet":
		return &dagconfig.SimnetParams, nil
	case "devnet", "hoosat-devnet":
		return &dagconfig.DevnetParams, nil
	default:
		return nil, errors.Errorf("unknown network %q (expected one of: mainnet, testnet, testnet-b5, testnet-b10, simnet, devnet)", network)
	}
}

func openDatabase(dbPath string, dbType string) (infrastructuredatabase.Database, error) {
	if strings.EqualFold(dbType, "leveldb") {
		return ldb.NewLevelDB(dbPath, leveldbCacheSizeMiB)
	}
	return pebble.NewPebbleDB(dbPath, pebbledbCacheSizeMiB)
}

// openConsensus opens the node's own on-disk database directly (read/write from the storage
// engine's point of view, but this tool never inserts blocks or otherwise mutates consensus
// state - it only calls read APIs). The node process must not be running against the same
// database directory at the same time, since the two would contend for the same underlying
// database files/locks.
func openConsensus(dbPath, dbType, network string) (externalapi.Consensus, infrastructuredatabase.Database, error) {
	params, err := netParamsByName(network)
	if err != nil {
		return nil, nil, err
	}

	db, err := openDatabase(dbPath, dbType)
	if err != nil {
		return nil, nil, errors.Wrapf(err, "failed to open database at %s (is the node still running against it?)", dbPath)
	}

	consensusConfig := consensus.Config{
		Params: *params,
		// Never let this tool trigger pruning deletions; it only reads.
		IsArchival: true,
	}
	mempoolConfig := mempool.DefaultConfig(&consensusConfig.Params)

	domainInstance, err := domain.New(&consensusConfig, mempoolConfig, db)
	if err != nil {
		db.Close()
		return nil, nil, err
	}

	return domainInstance.Consensus(), db, nil
}
