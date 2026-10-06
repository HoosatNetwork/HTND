package main

import (
	"fmt"
	"os"

	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/stability-tests/common"
	"github.com/HoosatNetwork/HTND/v2/util/panics"
)

var (
	backendLog = logger.NewBackend()
	log        = backendLog.Logger("KSSA")
	spawn      = panics.GoroutineWrapperFunc(log)
)

func initLog(logFile, errLogFile string) {
	level := logger.LevelDebug
	if activeConfig().LogLevel != "" {
		var ok bool
		level, ok = logger.LevelFromString(activeConfig().LogLevel)
		if !ok {
			fmt.Fprintf(os.Stderr, "Log level %s doesn't exists", activeConfig().LogLevel)
			os.Exit(1)
		}
	}
	log.SetLevel(level)
	common.InitBackend(backendLog, logFile, errLogFile)
}
