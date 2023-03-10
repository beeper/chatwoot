package main

import (
	"context"
	"time"

	"github.com/rs/zerolog"
)

// Simple crypto.Logger implementation that just prints to stdout.
type CryptoLogger struct {
	log *zerolog.Logger
}

func NewCryptoLogger(log *zerolog.Logger) *CryptoLogger {
	return &CryptoLogger{
		log: log,
	}
}

func (l CryptoLogger) Error(message string, args ...any) {
	l.log.Error().Msgf(message, args...)
}

func (l CryptoLogger) Warn(message string, args ...any) {
	l.log.Warn().Msgf(message, args...)
}

func (l CryptoLogger) Debug(message string, args ...any) {
	l.log.Debug().Msgf(message, args...)
}

func (l CryptoLogger) Trace(message string, args ...any) {
	l.log.Trace().Msgf(message, args...)
}

func (l CryptoLogger) QueryTiming(ctx context.Context, method, query string, args []any, duration time.Duration) {
	if duration > 1*time.Second {
		l.log.Warn().Str("method", method).Str("query", query).Dur("duration", duration).Msg("query took more than 1 second")
	}
}

func (l CryptoLogger) WarnUnsupportedVersion(current, latest int) {
	l.log.Warn().
		Int("current_version", current).
		Int("latest_version", latest).
		Msg("unsupported database schema version, but continuing anyway")
}

func (l CryptoLogger) PrepareUpgrade(current, latest int) {
	l.log.Info().
		Int("current_version", current).
		Int("latest_version", latest).
		Msg("preparing database upgrade")
}

func (l CryptoLogger) DoUpgrade(from, to int, message string) {
	l.log.Info().
		Int("from", from).
		Int("to", to).
		Str("msg", message).
		Msg("upgrading database")
}
