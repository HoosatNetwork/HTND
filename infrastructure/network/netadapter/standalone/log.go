package standalone

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("NTAR")
	spawn = panics.GoroutineWrapperFunc(log)
)
