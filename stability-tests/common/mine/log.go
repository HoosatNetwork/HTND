package mine

import (
	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
	"github.com/Hoosat-Oy/HTND/util/panics"
)

// log is a logger that is initialized with no output filters. This
// means the package will not perform any logging by default until the caller
// requests it.
var (
	log   *logger.Logger
	spawn func(name string, spawnedFunction func())
)

const logSubsytem = "MFJS"

// The default amount of logging is none.
func init() {
	DisableLog()
}

// DisableLog disables all library log output. Logging output is disabled
// by default until UseLogger is called.
func DisableLog() {
	backend := logger.NewBackend()
	log = backend.Logger(logSubsytem)
	log.SetLevel(logger.LevelOff)
	spawn = panics.GoroutineWrapperFunc(log)
}

// UseLogger uses a specified Logger to output package logging info.
func UseLogger(backend *logger.Backend, level logger.Level) {
	log = backend.Logger(logSubsytem)
	log.SetLevel(level)
	spawn = panics.GoroutineWrapperFunc(log)
}
