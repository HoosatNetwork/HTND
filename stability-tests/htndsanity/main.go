package main

import (
	"fmt"
	"os"

	"github.com/Hoosat-Oy/HTND/stability-tests/common"
	"github.com/Hoosat-Oy/HTND/util/profiling"

	"github.com/Hoosat-Oy/HTND/util/panics"
	"github.com/pkg/errors"
)

func main() {
	os.Exit(run())
}

func run() int {
	defer panics.HandlePanic(log, "htndsanity-main", nil)
	err := parseConfig()
	if err != nil {
		panic(errors.Wrap(err, "error in parseConfig"))
	}
	// backendLog.Close() is called explicitly before os.Exit(1) in all error paths.
	common.UseLogger(backendLog, log.Level())

	cfg := activeConfig()
	if cfg.Profile != "" {
		profiling.Start(cfg.Profile, log)
	}

	argsChan := readArgs()
	failures, err := commandLoop(argsChan)
	if err != nil {
		panic(errors.Wrap(err, "error in commandLoop"))
	}

	if len(failures) > 0 {
		fmt.Fprintf(os.Stderr, "FAILED:\n")
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, failure)
		}
		if backendLog != nil {
			backendLog.Close()
		}
		return 1
	}

	log.Infof("All tests have passed")
	return 0
}
