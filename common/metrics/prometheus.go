// Copyright 2019 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package prometheus exposes go-metrics into a Prometheus format.
package prometheus

import (
	"fmt"
	"net/http"
	"sort"
	"sync"

	metrics2 "github.com/VictoriaMetrics/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	"github.com/n42blockchain/N42/log"
)

var registerDefaultSetOnce sync.Once
var defaultSetGatherer prometheus.Gatherer
var defaultSetRegisterErr error

var extraCollectorsMu sync.Mutex
var extraCollectors []prometheus.Collector

// RegisterCollector adds a collector to the set this package's Handler serves.
// It exists because sibling metric packages keep their own default Sets, and a
// Set that nothing registers is collected on every write and readable nowhere
// -- which is what happened to lib/metrics for the whole life of the
// db_pgops family.
//
// Must be called before the first Handler(), which is when the gatherer is
// built. Collectors that fail to register (a duplicate metric name across two
// Sets is the likely cause) are logged and skipped rather than taking the
// endpoint down with them: one broken collector should cost its own metrics,
// not all of them.
func RegisterCollector(c prometheus.Collector) {
	if c == nil {
		return
	}
	extraCollectorsMu.Lock()
	extraCollectors = append(extraCollectors, c)
	extraCollectorsMu.Unlock()
}

// Handler returns an HTTP handler which dump metrics in Prometheus format.
// Output format can be checked here: https://o11y.tools/metricslint/
func Handler(reg Registry) http.Handler {
	registerDefaultSetOnce.Do(func() {
		registry := prometheus.NewRegistry()
		defaultSetRegisterErr = registry.Register(defaultSet)
		if defaultSetRegisterErr == nil {
			defaultSetGatherer = registry
		}
		extraCollectorsMu.Lock()
		extras := append([]prometheus.Collector(nil), extraCollectors...)
		extraCollectorsMu.Unlock()
		for _, c := range extras {
			if err := registry.Register(c); err != nil {
				log.Warn("metrics collector not registered; its metrics will not be served", "err", err)
				continue
			}
			if defaultSetGatherer == nil {
				defaultSetGatherer = registry
			}
		}
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Gather and pre-sort the metrics to avoid random listings
		var names []string
		reg.Each(func(name string, i interface{}) {
			names = append(names, name)
		})
		sort.Strings(names)

		w.Header().Set("Access-Control-Allow-Origin", "*")

		metrics2.WritePrometheus(w, false)

		if defaultSetRegisterErr == nil && defaultSetGatherer != nil {
			contentType := expfmt.Negotiate(r.Header)
			enc := expfmt.NewEncoder(w, contentType)
			mf, err := defaultSetGatherer.Gather()
			if err == nil {
				for _, m := range mf {
					if err := enc.Encode(m); err != nil {
						log.Warn("Failed to encode Prometheus default metric", "err", err)
						break
					}
				}
			}
		}

		// Aggregate all the metrics into a Prometheus collector
		c := newCollector()
		c.buff.WriteRune('\n')

		var typeName string
		var prevTypeName string

		for _, name := range names {
			i := reg.Get(name)

			typeName = stripLabels(name)

			switch m := i.(type) {
			case *metrics2.Counter:
				if m.IsGauge() {
					c.writeGauge(name, m.Get(), typeName != prevTypeName)
				} else {
					c.writeCounter(name, m.Get(), typeName != prevTypeName)
				}
			case *metrics2.Gauge:
				c.writeGauge(name, m, typeName != prevTypeName)
			case *metrics2.FloatCounter:
				c.writeFloatCounter(name, m, typeName != prevTypeName)
			case *metrics2.Histogram:
				c.writeHistogram(name, m, typeName != prevTypeName)
			case *metrics2.Summary:
				c.writeTimer(name, m, typeName != prevTypeName)
			default:
				log.Warn("Unknown Prometheus metric type", "type", fmt.Sprintf("%T", i))
			}

			prevTypeName = typeName
		}
		w.Header().Add("Content-Type", "text/plain")
		w.Header().Add("Content-Length", fmt.Sprint(c.buff.Len()))
		w.Write(c.buff.Bytes())
	})
}
