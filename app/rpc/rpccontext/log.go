package rpccontext

import "github.com/HoosatNetwork/HTND/v2/infrastructure/logger"

// Shares the RPC handlers' subsystem: these helpers run inside those handlers, so what they log
// belongs next to what the handlers log.
var log = logger.RegisterSubSystem("RPCS")
