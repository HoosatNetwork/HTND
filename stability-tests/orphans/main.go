package main

import (
	"fmt"
	"os"
	"time"

	"github.com/pkg/errors"

	"github.com/Hoosat-Oy/HTND/stability-tests/common"
	"github.com/Hoosat-Oy/HTND/stability-tests/common/rpc"
	"github.com/Hoosat-Oy/HTND/util/profiling"
)

var timeout = 10 * time.Minute

func main() {
	err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config: %+v", err)
		os.Exit(1)
	}
	// backendLog.Close() is called explicitly before os.Exit(1) in all error paths.
	common.UseLogger(backendLog, log.Level())
	cfg := activeConfig()
	if cfg.Profile != "" {
		profiling.Start(cfg.Profile, log)
	}

	blocks, topBlock, err := prepareBlocks()
	if err != nil {
		log.Errorf("Error preparing blocks: %+v", err)
		if backendLog != nil {
			backendLog.Close()
		}
		os.Exit(1)
	}

	routes := connectToNode()

	rpcClient, err := rpc.ConnectToRPC(&cfg.Config, cfg.NetParams())
	if err != nil {
		panic(errors.Wrap(err, "error connecting to JSON-RPC server"))
	}

	err = sendBlocks(routes, blocks, topBlock)
	if err != nil {
		if backendLog != nil {
			backendLog.Close()
		}
		_ = rpcClient.Disconnect()
		log.Errorf("Error sending blocks: %+v", err)
		os.Exit(1)
	}

	// Wait a second to let htnd process orphans
	<-time.After(1 * time.Second)

	err = checkTopBlockIsTip(rpcClient, topBlock)
	if err != nil {
		log.Errorf("Error in checkTopBlockIsTip: %+v", err)
		if backendLog != nil {
			backendLog.Close()
		}
		_ = rpcClient.Disconnect()
		os.Exit(1)
	}
}
