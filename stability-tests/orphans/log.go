package main

import (
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
)

var (
	backendLog = logger.NewBackend()
	log        = backendLog.Logger("ORPH")
)
