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

package parallel

import "bytes"

// Validate checks whether a transaction's read set is still consistent
// with the current MVS state. If any read has become stale (because a
// preceding transaction wrote a different value or was re-executed with
// a new incarnation after this tx executed), the transaction must be
// re-executed.
//
// Returns true if the read set is valid, false if re-execution is needed.
func Validate(mvs *MVS, rw *ReadWriteSet) bool {
	for _, rd := range rw.Reads {
		cur, writerTx, writerInc, found := mvs.Read(rd.Key, rw.TxIndex)
		if rd.FromBase {
			// Value was read from base DB. Still valid if no preceding tx
			// has since written this location, or if what it wrote is
			// byte-for-byte what the base held.
			if !found {
				continue
			}
			if rd.HasValue && bytes.Equal(cur, rd.Value) {
				continue
			}
			return false
		}
		// Value was read from MVS (written by tx[rd.WriterTx]).
		if !found {
			// The write we depended on was removed — stale.
			return false
		}
		if writerTx == rd.WriterTx && writerInc == rd.WriterIncarnation {
			continue
		}
		// A different writer, or the same writer re-executed. The read is
		// still valid if the bytes are the same: a sender's nonce chain does
		// not change because its predecessor re-ran over a recipient
		// conflict, and version-only validation cascaded that re-run down
		// every chain (round 35d, 64-wave limit at 15k transactions).
		if rd.HasValue && bytes.Equal(cur, rd.Value) {
			continue
		}
		return false
	}
	return true
}
