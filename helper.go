package main

import (
	"errors"
	"fmt"
	_ "strconv"
	"time"

	"github.com/sethvargo/go-retry"
	log "github.com/sirupsen/logrus"
)

func DoRetry(description string, fn func() (interface{}, error)) (interface{}, error) {
	b, err := retry.NewFibonacci(1 * time.Second)
	if err != nil {
		panic(err)
	}
	b = retry.WithMaxRetries(5, b)
	for {
		log.Info("trying: ", description)
		val, err := fn()
		if err == nil {
			log.Info(description, " succeeded")
			return val, nil
		}
		nextDuration, stop := b.Next()
		log.Debugf("  %s failed (%+v). Retrying in %f seconds...", description, err, nextDuration.Seconds())
		if stop {
			log.Debugf("  %s failed (%+v). Retry limit reached. Will not retry.", description, err)
			return nil, errors.New(fmt.Sprintf("%s failed (%+v). Retry limit reached. Will not retry.", description, err))
		}
		time.Sleep(nextDuration)
	}
}
