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
// pprof profiling endpoint configuration.
// PprofConfig toggles the net/http/pprof handler, its listen Port
// and runtime tuning flags (MaxCpu GOMAXPROCS, TraceMutex,
// TraceBlock) used to expose CPU, mutex and block profiles to
// external collectors like go tool pprof.

package conf

type PprofConfig struct {
	MaxCpu     int  `json:"cpu" yaml:"cpu"`
	Port       int  `json:"port" yaml:"port"`
	TraceMutex bool `json:"trace_mutex" yaml:"trace_mutex"`
	TraceBlock bool `json:"trace_block" yaml:"trace_block"`
	Pprof      bool `json:"pprof" yaml:"pprof"`

	// MutexFraction is the 1-in-N sampling fraction for the mutex profile.
	// Enabling mutex profiling used to hardcode 1, which records EVERY
	// contention event: on a node under load that is enough overhead to change
	// the contention being measured. 100 keeps the shape of the profile while
	// costing little. 1 is still available for a quiet node where completeness
	// matters more than fidelity.
	MutexFraction int `json:"mutex_fraction" yaml:"mutex_fraction"`

	// BlockRateNanos is the target average interval, in nanoseconds of blocked
	// time, between sampled blocking events. Enabling block profiling used to
	// hardcode 1, i.e. sample every channel operation and lock wait in the
	// process. 10000 (10 microseconds) skips the noise floor and still catches
	// anything worth calling a stall.
	BlockRateNanos int `json:"block_rate_nanos" yaml:"block_rate_nanos"`
}

// Defaults for the profiling rates, applied when the value is left at zero.
const (
	DefaultMutexFraction  = 100
	DefaultBlockRateNanos = 10000
)

// MutexProfileFraction returns the configured fraction, or the default.
func (c *PprofConfig) MutexProfileFraction() int {
	if c.MutexFraction > 0 {
		return c.MutexFraction
	}
	return DefaultMutexFraction
}

// BlockProfileRate returns the configured rate, or the default.
func (c *PprofConfig) BlockProfileRate() int {
	if c.BlockRateNanos > 0 {
		return c.BlockRateNanos
	}
	return DefaultBlockRateNanos
}
