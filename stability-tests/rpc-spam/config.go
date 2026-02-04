package rpcspam

import (
	"path/filepath"
	"time"

	"github.com/Hoosat-Oy/HTND/stability-tests/common"
	"github.com/jessevdk/go-flags"
)

const (
	defaultLogFilename    = "rpc-spam.log"
	defaultErrLogFilename = "rpc-spam_err.log"
)

var (
	defaultLogFile    = filepath.Join(common.DefaultAppDir, defaultLogFilename)
	defaultErrLogFile = filepath.Join(common.DefaultAppDir, defaultErrLogFilename)
)

type configFlags struct {
	LogLevel         string        `long:"loglevel" description:"Set log level {trace, debug, info, warn, error, critical}"`
	Profile          string        `long:"profile" description:"Enable HTTP profiling on given port -- NOTE port must be between 1024 and 65536"`
	RPCAddress       string        `long:"rpc-address" description:"RPC address of the htnd node"`
	StartNode        bool          `long:"start-node" description:"Start a temporary htnd node for the test (devnet)"`
	Duration         time.Duration `long:"duration" description:"How long to spam RPC requests (e.g. 30s, 2m)" default:"30s"`
	ProgressInterval time.Duration `long:"progress-interval" description:"How often to print progress logs while running" default:"5s"`
	Workers          int           `long:"workers" description:"Number of concurrent request goroutines" default:"50"`
	Clients          int           `long:"clients" description:"Number of separate RPC connections to open" default:"1"`
	TotalCalls       int           `long:"calls" description:"Total RPC calls to issue (0 = run for --duration)" default:"0"`
	Methods          string        `long:"methods" description:"Comma-separated list of RPC calls to use: GetInfo,GetBlockCount,GetBlockDAGInfo,GetCoinSupply,GetVirtualSelectedParentBlueScore,GetSelectedTipHash,GetBlock" default:"GetInfo,GetBlockCount,GetBlockDAGInfo"`
	MaxErrors        int           `long:"max-errors" description:"Fail if total errors exceed this (0 = no errors allowed)" default:"10"`
	MaxErrorRate     float64       `long:"max-error-rate" description:"Fail if errors/total exceed this" default:"0.001"`
	PerCallTimeout   time.Duration `long:"per-call-timeout" description:"RPC timeout per call" default:"5s"`
}

var cfg *configFlags

func activeConfig() *configFlags {
	return cfg
}

func parseConfig() error {
	cfg = &configFlags{RPCAddress: "localhost:9000"}
	parser := flags.NewParser(cfg, flags.PrintErrors|flags.HelpFlag|flags.IgnoreUnknown)
	_, err := parser.Parse()
	if err != nil {
		return err
	}

	initLog(defaultLogFile, defaultErrLogFile)
	return nil
}
