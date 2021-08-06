package main

import (
	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix/crypto"
)

// Simple crypto.Logger implementation that just prints to stdout.
type CryptoLogger struct{}

var _ crypto.Logger = &CryptoLogger{}

func (f CryptoLogger) Error(message string, args ...interface{}) {
	log.Errorf(message, args...)
}

func (f CryptoLogger) Warn(message string, args ...interface{}) {
	log.Warnf(message, args...)
}

func (f CryptoLogger) Debug(message string, args ...interface{}) {
	log.Debugf(message, args...)
}

func (f CryptoLogger) Trace(message string, args ...interface{}) {
	log.Tracef(message, args...)
}
