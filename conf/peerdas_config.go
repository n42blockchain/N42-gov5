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
// PeerDAS (EIP-7594) data availability sampling configuration.
// PeerDASConfig toggles DAS with CustodyCount columns (default 4)
// and SamplingEnabled / SampleCount (default 8) for spot-check
// queries. MaxPeerDASCustodyCount / MaxPeerDASSampleCount cap the
// values at 128 to keep the sample set bounded.

package conf

import "fmt"

const (
	DefaultPeerDASCustodyCount = 4
	DefaultPeerDASSampleCount  = 8
	MaxPeerDASCustodyCount     = 128
	MaxPeerDASSampleCount      = 128
)

// PeerDASConfig holds configuration for PeerDAS (EIP-7594) data
// availability sampling. Disabled by default.
type PeerDASConfig struct {
	Enable          bool   `json:"enable" yaml:"enable"`
	CustodyCount    uint64 `json:"custody_count" yaml:"custody_count"`       // columns to custody; default 4
	SamplingEnabled bool   `json:"das_sampling" yaml:"das_sampling"`         // run DAS checks on new blocks
	SampleCount     int    `json:"das_sample_count" yaml:"das_sample_count"` // columns per sample; default 8
}

// IsEnabled returns true if PeerDAS is explicitly enabled.
func (c *PeerDASConfig) IsEnabled() bool {
	return c.Enable
}

// Validate checks configuration values and applies defaults where needed.
func (c *PeerDASConfig) Validate() error {
	if !c.Enable {
		return nil
	}
	if c.CustodyCount == 0 {
		c.CustodyCount = DefaultPeerDASCustodyCount
	}
	if c.CustodyCount > MaxPeerDASCustodyCount {
		return fmt.Errorf("peerdas: CustodyCount %d exceeds max %d", c.CustodyCount, MaxPeerDASCustodyCount)
	}
	if c.SampleCount == 0 {
		c.SampleCount = DefaultPeerDASSampleCount
	}
	if c.SampleCount > MaxPeerDASSampleCount {
		return fmt.Errorf("peerdas: SampleCount %d exceeds max %d", c.SampleCount, MaxPeerDASSampleCount)
	}
	return nil
}
