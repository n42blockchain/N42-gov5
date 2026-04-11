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
// StorageSize float-backed human-readable byte-size type. String
// and TerminalString scale to B / KiB / MiB / GiB / TiB with two
// decimal places, using the kiB / miB / giB / tiB power-of-two
// constants for thresholds. Used by log output, RPC responses and
// metrics gauges that expose database or cache sizes.

package types

import "fmt"

const (
	kiB StorageSize = 1024
	miB             = 1024 * kiB
	giB             = 1024 * miB
	tiB             = 1024 * giB
)

type StorageSize float64

func (s StorageSize) String() string {
	if s > tiB {
		return fmt.Sprintf("%.2f TiB", s/tiB)
	}
	if s > giB {
		return fmt.Sprintf("%.2f GiB", s/giB)
	}
	if s > miB {
		return fmt.Sprintf("%.2f MiB", s/miB)
	}
	if s > kiB {
		return fmt.Sprintf("%.2f KiB", s/kiB)
	}
	return fmt.Sprintf("%.2f B", s)
}

func (s StorageSize) TerminalString() string {
	if s > tiB {
		return fmt.Sprintf("%.2fTiB", s/tiB)
	}
	if s > giB {
		return fmt.Sprintf("%.2fGiB", s/giB)
	}
	if s > miB {
		return fmt.Sprintf("%.2fMiB", s/miB)
	}
	if s > kiB {
		return fmt.Sprintf("%.2fKiB", s/kiB)
	}
	return fmt.Sprintf("%.2fB", s)
}
