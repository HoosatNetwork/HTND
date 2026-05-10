package txscript

import (
	"os"
	"testing"

	"github.com/Hoosat-Oy/HTND/infrastructure/logger"
)

func TestMain(m *testing.M) {
	// Default to a quiet logger during tests so failures are readable and output
	// doesn't get truncated by CI/log collectors.
	// Set TXSCRIPT_TRACE=1 to re-enable trace-level logs when debugging.
	level := logger.LevelError
	if os.Getenv("TXSCRIPT_TRACE") == "1" {
		level = logger.LevelTrace
	}
	log.SetLevel(level)
	logger.InitLogStdout(level)

	os.Exit(m.Run())
}
