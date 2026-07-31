package main

import (
	"fmt"
	"os"

	"github.com/HoosatNetwork/HTND/infrastructure/config"
	"github.com/HoosatNetwork/HTND/infrastructure/network/netadapter/standalone"
	"github.com/HoosatNetwork/HTND/stability-tests/common"
	"github.com/HoosatNetwork/HTND/util/panics"
	"github.com/HoosatNetwork/HTND/util/profiling"
)

func main() {
	// panics.HandlePanic is called explicitly before os.Exit(1) in all error paths.
	err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config: %+v", err)
		panics.HandlePanic(log, "applicationLevelGarbage-main", nil)
		os.Exit(1)
	}
	common.UseLogger(backendLog, log.Level())
	cfg := activeConfig()
	if cfg.Profile != "" {
		profiling.Start(cfg.Profile, log)
	}

	htndConfig := config.DefaultConfig()
	htndConfig.NetworkFlags = cfg.NetworkFlags

	minimalNetAdapter, err := standalone.NewMinimalNetAdapter(htndConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating minimalNetAdapter: %+v", err)
		backendLog.Close()
		panics.HandlePanic(log, "applicationLevelGarbage-main", nil)
		os.Exit(1)
	}

	blocksChan, err := readBlocks()
	if err != nil {
		log.Errorf("Error reading blocks: %+v", err)
		backendLog.Close()
		panics.HandlePanic(log, "applicationLevelGarbage-main", nil)
		os.Exit(1)
	}

	err = sendBlocks(cfg.NodeP2PAddress, minimalNetAdapter, blocksChan)
	if err != nil {
		log.Errorf("Error sending blocks: %+v", err)
		backendLog.Close()
		os.Exit(1)
	}
	backendLog.Close()
}
