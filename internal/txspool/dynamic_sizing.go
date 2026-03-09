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

package txspool

import (
	"runtime"
	"time"

	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/log"
)

const (
	// dynamicSizingInterval is how often the pool checks memory pressure.
	dynamicSizingInterval = 30 * time.Second

	// memoryHighWatermark triggers slot reduction (85% of limit).
	memoryHighWatermark = 85

	// memoryLowWatermark restores slots (70% of limit).
	memoryLowWatermark = 70
)

var (
	txpoolEffectiveSlots = prometheus.GetOrCreateCounter("txpool_effective_slots", true)
	txpoolMemoryUsedMB   = prometheus.GetOrCreateCounter("txpool_memory_used_mb", true)
)

// dynamicSizingLoop periodically adjusts the effective pool capacity based on
// the process heap memory usage. When heap usage exceeds the high watermark,
// the effective slots are scaled down proportionally. When it drops below the
// low watermark, slots are gradually restored to the configured maximum.
//
// The algorithm:
//   - heapMB >= limit*85%  → scale = limit*70% / heapMB (clamped to [min, max])
//   - heapMB < limit*70%   → restore to max
//   - in between           → hold current value (hysteresis)
func (pool *TxsPool) dynamicSizingLoop() {
	defer pool.wg.Done()

	maxSlots := pool.config.GlobalSlots
	minSlots := pool.config.MinGlobalSlots
	limitMB := pool.config.MemoryLimitMB

	if minSlots == 0 {
		minSlots = 1024
	}
	if limitMB == 0 {
		limitMB = 4096
	}
	if minSlots > maxSlots {
		minSlots = maxSlots
	}

	highMB := limitMB * memoryHighWatermark / 100
	lowMB := limitMB * memoryLowWatermark / 100

	ticker := time.NewTicker(dynamicSizingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			heapMB := m.HeapInuse / (1024 * 1024)

			txpoolMemoryUsedMB.Set(heapMB)

			current := pool.effectiveGlobalSlots.Load()
			var target uint64

			if heapMB >= highMB {
				// Scale down: target = maxSlots * (lowMB / heapMB)
				target = maxSlots * lowMB / heapMB
				if target < minSlots {
					target = minSlots
				}
			} else if heapMB < lowMB {
				// Restore to max
				target = maxSlots
			} else {
				// Hysteresis zone: no change
				target = current
			}

			if target != current {
				pool.effectiveGlobalSlots.Store(target)
				txpoolEffectiveSlots.Set(target)
				if target < current {
					log.Warn("TxPool dynamic sizing: reducing capacity due to memory pressure",
						"heapMB", heapMB, "limitMB", limitMB,
						"slots", current, "newSlots", target)
				} else {
					log.Info("TxPool dynamic sizing: restoring capacity",
						"heapMB", heapMB, "slots", current, "newSlots", target)
				}
			}

		case <-pool.ctx.Done():
			return
		}
	}
}

// EffectiveGlobalSlots returns the current effective global slots limit.
// When dynamic sizing is disabled, this returns the configured GlobalSlots.
func (pool *TxsPool) EffectiveGlobalSlots() uint64 {
	if !pool.config.DynamicSizing {
		return pool.config.GlobalSlots
	}
	return pool.effectiveGlobalSlots.Load()
}
