package handshake

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("PROT")
	spawn = panics.GoroutineWrapperFunc(log)
)
