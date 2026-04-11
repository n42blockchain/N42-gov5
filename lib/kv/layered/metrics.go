// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Prometheus bridge for the layered KV cache. Declares the
// layered_cache_hits_total, layered_cache_misses_total,
// layered_cache_entries and layered_cache_hit_rate gauges and
// provides ShardedCache.CollectMetrics, which reads Stats() and
// pushes the values into the gauges along with the derived hit
// ratio. LayeredDB.CollectMetrics is a thin wrapper that lets a
// background ticker or RwTx hook update cache metrics for the
// whole layered database with a single call.

package layered

import "github.com/n42blockchain/N42/lib/metrics"

var (
	cacheHitsGauge    = metrics.GetOrCreateGauge(`layered_cache_hits_total`)
	cacheMissesGauge  = metrics.GetOrCreateGauge(`layered_cache_misses_total`)
	cacheEntriesGauge = metrics.GetOrCreateGauge(`layered_cache_entries`)
	cacheHitRateGauge = metrics.GetOrCreateGauge(`layered_cache_hit_rate`)
)

// CollectMetrics pushes current cache stats to Prometheus gauges.
// Call this periodically (e.g. from RwTx.CollectMetrics or a background ticker).
func (c *ShardedCache) CollectMetrics() {
	hits, misses, entries := c.Stats()
	cacheHitsGauge.SetUint64(hits)
	cacheMissesGauge.SetUint64(misses)
	cacheEntriesGauge.SetInt(entries)

	total := hits + misses
	if total > 0 {
		cacheHitRateGauge.Set(float64(hits) / float64(total))
	}
}

// CollectMetrics collects metrics for both underlying DBs and the cache.
func (db *LayeredDB) CollectMetrics() {
	db.cache.CollectMetrics()
}
