package connmanager

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("CMGR")
	spawn = panics.GoroutineWrapperFunc(log)
)
