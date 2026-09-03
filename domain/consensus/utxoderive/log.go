package utxoderive

import "github.com/HoosatNetwork/HTND/infrastructure/logger"

// UTXD is the derive replay's own subsystem, kept separate from BDAG so that a long replay's
// per-block mismatch record can be filtered on without dragging in consensus logging.
var log = logger.RegisterSubSystem("UTXD")
