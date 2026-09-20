package standalone

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("NTAR")
	spawn = panics.GoroutineWrapperFunc(log)
)
