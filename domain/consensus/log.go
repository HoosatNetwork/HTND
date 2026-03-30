package consensus

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("BDAG")
	spawn = panics.GoroutineWrapperFunc(log)
)
