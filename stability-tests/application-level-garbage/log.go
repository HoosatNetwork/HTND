package main

import (
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/util/panics"
)

var (
	backendLog = logger.NewBackend()
	log        = backendLog.Logger("APLG")
	spawn      = panics.GoroutineWrapperFunc(log)
)
