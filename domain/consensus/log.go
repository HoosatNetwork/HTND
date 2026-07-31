package consensus

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("BDAG")
	spawn = panics.GoroutineWrapperFunc(log)
)
