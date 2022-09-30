package main

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
	"maunium.net/go/mautrix/crypto"
)

// Simple crypto.Logger implementation that just prints to stdout.
type CryptoLogger struct{}

var _ crypto.Logger = &CryptoLogger{}

func (l CryptoLogger) Error(message string, args ...interface{}) {
	log.Errorf(message, args...)
}

func (l CryptoLogger) Warn(message string, args ...interface{}) {
	log.Warnf(message, args...)
}

func (l CryptoLogger) Debug(message string, args ...interface{}) {
	log.Debugf(message, args...)
}

func (l CryptoLogger) Trace(message string, args ...interface{}) {
	log.Tracef(message, args...)
}

func (l CryptoLogger) QueryTiming(ctx context.Context, method, query string, args []interface{}, duration time.Duration) {
	if duration > 1*time.Second {
		log.Warnf("%s(%s) took %.3f seconds", method, query, duration.Seconds())
	}
}

func (l CryptoLogger) WarnUnsupportedVersion(current, latest int) {
	log.Warnf("Unsupported database schema version: currently on v%d, latest known: v%d - continuing anyway", current, latest)
}

func (l CryptoLogger) PrepareUpgrade(current, latest int) {
	log.Infof("Database currently on v%d, latest: v%d", current, latest)
}

func (l CryptoLogger) DoUpgrade(from, to int, message string) {
	log.Infof("Upgrading database from v%d to v%d: %s", from, to, message)
}
