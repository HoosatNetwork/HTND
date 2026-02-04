package pebble

import "github.com/cockroachdb/pebble/v2"

// Ensure pebbleLoggerAdapter implements pebble.Logger.
var _ pebble.Logger = (*pebbleLoggerAdapter)(nil)

type pebbleLoggerAdapter struct{}

func (pebbleLoggerAdapter) Infof(format string, args ...interface{}) {
	log.Infof("[pebble] "+format, args...)
}

func (pebbleLoggerAdapter) Errorf(format string, args ...interface{}) {
	log.Errorf("[pebble] "+format, args...)
}

func (pebbleLoggerAdapter) Fatalf(format string, args ...interface{}) {
	log.Criticalf("[pebble] "+format, args...)
}
