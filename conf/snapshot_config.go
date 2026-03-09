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

package conf

import "fmt"

const (
	DefaultSnapshotInterval     = uint64(10000) // create snapshot every N blocks
	DefaultSnapshotMaxRetained  = 3             // keep at most N snapshots
	DefaultSnapshotSafetyMargin = uint64(64)    // snapshot height = HEAD - margin
)

// SnapshotConfig controls the periodic state snapshot system.
// Snapshots mark block heights where the complete PlainState is recoverable
// via current state + changesets. The pruner respects snapshot boundaries so
// that changesets between the oldest retained snapshot and HEAD are never deleted.
type SnapshotConfig struct {
	Enable        bool   `json:"enable" yaml:"enable"`
	Interval      uint64 `json:"interval" yaml:"interval"`             // blocks between snapshots
	MaxRetained   int    `json:"max_retained" yaml:"max_retained"`     // max snapshots to keep
	SafetyMargin  uint64 `json:"safety_margin" yaml:"safety_margin"`   // HEAD - margin = snapshot height
}

// Validate checks that configuration values are within acceptable ranges
// and applies defaults where needed.
func (c *SnapshotConfig) Validate() error {
	if !c.Enable {
		return nil
	}
	if c.Interval == 0 {
		c.Interval = DefaultSnapshotInterval
	}
	if c.Interval < 100 {
		return fmt.Errorf("snapshot: Interval %d below minimum 100", c.Interval)
	}
	if c.MaxRetained == 0 {
		c.MaxRetained = DefaultSnapshotMaxRetained
	}
	if c.MaxRetained < 1 || c.MaxRetained > 100 {
		return fmt.Errorf("snapshot: MaxRetained %d outside range [1, 100]", c.MaxRetained)
	}
	if c.SafetyMargin == 0 {
		c.SafetyMargin = DefaultSnapshotSafetyMargin
	}
	return nil
}
