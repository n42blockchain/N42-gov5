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

package common

import "fmt"

// StorageSize is a wrapper around a float value that supports user friendly
// formatting.
type StorageSize float64

const (
	storageKiB = 1024
	storageMiB = 1024 * storageKiB
	storageGiB = 1024 * storageMiB
	storageTiB = 1024 * storageGiB
)

// String implements the stringer interface.
func (s StorageSize) String() string {
	if s > storageTiB {
		return fmt.Sprintf("%.2f TiB", s/storageTiB)
	}
	if s > storageGiB {
		return fmt.Sprintf("%.2f GiB", s/storageGiB)
	}
	if s > storageMiB {
		return fmt.Sprintf("%.2f MiB", s/storageMiB)
	}
	if s > storageKiB {
		return fmt.Sprintf("%.2f KiB", s/storageKiB)
	}
	return fmt.Sprintf("%.2f B", s)
}

// TerminalString implements log.TerminalStringer, formatting a string for console
// output during logging.
func (s StorageSize) TerminalString() string {
	if s > storageTiB {
		return fmt.Sprintf("%.2fTiB", s/storageTiB)
	}
	if s > storageGiB {
		return fmt.Sprintf("%.2fGiB", s/storageGiB)
	}
	if s > storageMiB {
		return fmt.Sprintf("%.2fMiB", s/storageMiB)
	}
	if s > storageKiB {
		return fmt.Sprintf("%.2fKiB", s/storageKiB)
	}
	return fmt.Sprintf("%.2fB", s)
}
