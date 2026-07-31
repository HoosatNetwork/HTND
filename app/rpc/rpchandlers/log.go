package rpchandlers

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("RPCS")
	spawn = panics.GoroutineWrapperFunc(log)
)
