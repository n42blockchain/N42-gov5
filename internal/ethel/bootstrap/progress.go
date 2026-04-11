// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bootstrap

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/internal/ethel/fetch"
	"github.com/n42blockchain/N42/log"
)

// progressLogger throttles per-asset download progress so the log does
// not get spammed once per MB. It emits one line per asset at most every
// logInterval, plus a final line when the asset reports 100%.
type progressLogger struct {
	mu       sync.Mutex
	lastLog  map[string]time.Time
	interval time.Duration
}

const logInterval = 5 * time.Second

func newProgressLogger() *progressLogger {
	return &progressLogger{
		lastLog:  make(map[string]time.Time),
		interval: logInterval,
	}
}

// callback returns a ProgressFunc bound to assetName. Returning a
// closure lets the logger treat every asset as an independent throttle
// slot so a burst on one asset never masks another.
func (p *progressLogger) callback(assetName string) fetch.ProgressFunc {
	return func(pr fetch.Progress) {
		p.mu.Lock()
		defer p.mu.Unlock()

		done := pr.Total > 0 && pr.Bytes >= pr.Total
		last := p.lastLog[assetName]
		if !done && time.Since(last) < p.interval {
			return
		}
		p.lastLog[assetName] = time.Now()

		if pr.Total == 0 {
			log.Info("eth-el: download progress",
				"asset", assetName,
				"bytes_mb", pr.Bytes/(1<<20),
				"source", pr.Source)
			return
		}
		pct := float64(pr.Bytes) * 100 / float64(pr.Total)
		log.Info("eth-el: download progress",
			"asset", assetName,
			"pct", int(pct),
			"bytes_mb", pr.Bytes/(1<<20),
			"total_mb", pr.Total/(1<<20),
			"source", pr.Source)
	}
}
