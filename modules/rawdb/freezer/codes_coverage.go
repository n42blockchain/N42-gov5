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

package freezer

import (
	"encoding/binary"
	"os"
	"path/filepath"
)

// CodesCoverageFile is the sidecar that binds a code store to a block height.
//
// Contract bytecode is content-addressed (codeHash = keccak(code)) and therefore
// height-INDEPENDENT: a given codeHash always maps to the same bytes for every
// block. So individual codes need no height. What IS height-dependent is the
// store's COVERAGE — whether it contains every code deployed/live up to a given
// block. A code store built from a state snapshot at height H, or by executing
// to block N, is complete only up to that point; a verifier replaying block M
// must have a store covering M, else a contract first deployed after the store's
// height is simply absent. This single value records that boundary.
const CodesCoverageFile = "codes.coverage"

// WriteCodesCoverage records that the code store in dir contains every code
// deployed/live up to (and including) block. Call it from the builder with the
// source state's height (or the highest executed block).
func WriteCodesCoverage(dir string, block uint64) error {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], block)
	return os.WriteFile(filepath.Join(dir, CodesCoverageFile), b[:], 0o644)
}

// ReadCodesCoverage returns the recorded coverage height and whether the sidecar
// exists. A consumer that needs codes for block N should require
// (covered, ok) with ok && covered >= N before trusting the store; otherwise a
// missing/stale code read becomes a confusing execution divergence rather than a
// clear "store does not cover this height".
func ReadCodesCoverage(dir string) (uint64, bool) {
	b, err := os.ReadFile(filepath.Join(dir, CodesCoverageFile))
	if err != nil || len(b) < 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(b[:8]), true
}
