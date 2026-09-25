package utxo

import (
	"github.com/HoosatNetwork/HTND/v2/infrastructure/logger"
	"github.com/HoosatNetwork/HTND/v2/util/panics"
)

var (
	log   = logger.RegisterSubSystem("UTXO")
	spawn = panics.GoroutineWrapperFunc(log)
)
