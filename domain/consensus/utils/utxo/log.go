package utxo

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("UTXO")
	spawn = panics.GoroutineWrapperFunc(log)
)
