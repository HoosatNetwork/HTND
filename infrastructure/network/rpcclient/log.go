package rpcclient

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("RPCC")
	spawn = panics.GoroutineWrapperFunc(log)
)
