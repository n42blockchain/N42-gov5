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

package avmutil

import (
	"fmt"
)

// StorageSize is a wrapper around a float value that supports user friendly
// formatting.
type StorageSize float64

const (
	_TiB = 1 << 40
	_GiB = 1 << 30
	_MiB = 1 << 20
	_KiB = 1 << 10
)

// String implements the stringer interface.
func (s StorageSize) String() string {
	if s > _TiB {
		return fmt.Sprintf("%.2f TiB", s/_TiB)
	}
	if s > _GiB {
		return fmt.Sprintf("%.2f GiB", s/_GiB)
	}
	if s > _MiB {
		return fmt.Sprintf("%.2f MiB", s/_MiB)
	}
	if s > _KiB {
		return fmt.Sprintf("%.2f KiB", s/_KiB)
	}
	return fmt.Sprintf("%.2f B", s)
}

// TerminalString implements log.TerminalStringer, formatting a string for console
// output during logging.
func (s StorageSize) TerminalString() string {
	if s > _TiB {
		return fmt.Sprintf("%.2fTiB", s/_TiB)
	}
	if s > _GiB {
		return fmt.Sprintf("%.2fGiB", s/_GiB)
	}
	if s > _MiB {
		return fmt.Sprintf("%.2fMiB", s/_MiB)
	}
	if s > _KiB {
		return fmt.Sprintf("%.2fKiB", s/_KiB)
	}
	return fmt.Sprintf("%.2fB", s)
}
