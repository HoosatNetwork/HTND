package grpcclient

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("RPCC")
	spawn = panics.GoroutineWrapperFunc(log)
)
