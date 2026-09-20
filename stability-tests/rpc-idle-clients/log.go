package main

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

var (
	backendLog = logger.NewBackend()
	log        = backendLog.Logger("RPIC")
)
