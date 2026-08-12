package blockrelay

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("PROT")
	spawn = panics.GoroutineWrapperFunc(log)
)
