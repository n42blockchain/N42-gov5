// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package catchup

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/internal/ethel/fetch"
	"github.com/n42blockchain/N42/log"
)

// progressLogger is the catch-up counterpart of bootstrap.progressLogger.
// It is a separate type — not a shared helper — because the catchup
// service emits slightly different log keys ("segment" vs "asset") and
// because sharing would force one package to depend on the other with
// no structural gain.
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

func (p *progressLogger) callback(segmentName string) fetch.ProgressFunc {
	return func(pr fetch.Progress) {
		p.mu.Lock()
		defer p.mu.Unlock()

		done := pr.Total > 0 && pr.Bytes >= pr.Total
		last := p.lastLog[segmentName]
		if !done && time.Since(last) < p.interval {
			return
		}
		p.lastLog[segmentName] = time.Now()

		if pr.Total == 0 {
			log.Info("eth-el: segment progress",
				"segment", segmentName,
				"bytes_mb", pr.Bytes/(1<<20),
				"source", pr.Source)
			return
		}
		pct := float64(pr.Bytes) * 100 / float64(pr.Total)
		log.Info("eth-el: segment progress",
			"segment", segmentName,
			"pct", int(pct),
			"bytes_mb", pr.Bytes/(1<<20),
			"total_mb", pr.Total/(1<<20),
			"source", pr.Source)
	}
}
