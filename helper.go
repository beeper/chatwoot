package main

import (
	"context"
	_ "strconv"
	"time"

	"github.com/rs/zerolog"
	"github.com/sethvargo/go-retry"
)

func DoRetry[T any](ctx context.Context, description string, fn func(context.Context) (*T, error)) (*T, error) {
	log := zerolog.Ctx(ctx).With().Str("do_retry", description).Logger()
	var err error
	b, err := retry.NewFibonacci(1 * time.Second)
	if err != nil {
		panic(err)
	}
	b = retry.WithMaxRetries(5, b)
	attemptNum := 0
	for {
		attemptNum++
		attemptLogger := log.With().Int("attempt", attemptNum).Logger()
		attemptLogger.Debug().Msg("trying")
		var val *T
		val, err = fn(attemptLogger.WithContext(ctx))
		if err == nil {
			attemptLogger.Debug().Msg("succeeded")
			return val, nil
		}
		nextDuration, stop := b.Next()
		attemptLogger.Info().Err(err).
			Float64("retry_in_sec", nextDuration.Seconds()).
			Msg("failed")
		if stop {
			attemptLogger.Warn().Err(err).
				Msg("failed. Retry limit reached. Will not retry.")
			break
		}
		time.Sleep(nextDuration)
	}
	return nil, err
}
