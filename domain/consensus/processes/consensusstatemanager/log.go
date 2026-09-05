package consensusstatemanager

import (
	"github.com/HoosatNetwork/HTND/domain/consensus/utils/utxosurvey"
	"github.com/HoosatNetwork/HTND/infrastructure/logger"
)

var log = logger.RegisterSubSystem("BDAG")

func init() {
	// The survey package owns no logger of its own so that it stays a leaf dependency; it reports
	// where it is writing, and any problem writing there, through this one.
	utxosurvey.SetLogger(func(format string, args ...any) {
		log.Warnf(format, args...)
	})
}
