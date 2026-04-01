package connmanager

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("CMGR")
	spawn = panics.GoroutineWrapperFunc(log)
)
