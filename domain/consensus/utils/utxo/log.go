package utxo

import (
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/util/panics"
)

var (
	log   = logger.RegisterSubSystem("UTXO")
	spawn = panics.GoroutineWrapperFunc(log)
)
