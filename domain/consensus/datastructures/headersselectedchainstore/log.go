package headersselectedchainstore

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("HSCS")
	spawn = panics.GoroutineWrapperFunc(log)
)
