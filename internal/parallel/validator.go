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

// Validate checks whether a transaction's read set is still consistent
// with the current MVS state. If any read has become stale (because a
// preceding transaction wrote a different value or was re-executed with
// a new incarnation after this tx executed), the transaction must be
// re-executed.
//
// Returns true if the read set is valid, false if re-execution is needed.
func Validate(mvs *MVS, rw *ReadWriteSet) bool {
	for _, rd := range rw.Reads {
		if rd.FromBase {
			// Value was read from base DB. Check if any preceding tx
			// has since written to this location.
			_, _, _, found := mvs.Read(rd.Key, rw.TxIndex)
			if found {
				// A preceding tx now writes this location — stale.
				return false
			}
		} else {
			// Value was read from MVS (written by tx[rd.WriterTx]).
			// Verify the latest writer before our txIndex is still the same
			// AND has the same incarnation (value hasn't changed due to re-execution).
			_, writerTx, writerInc, found := mvs.Read(rd.Key, rw.TxIndex)
			if !found {
				// The write we depended on was removed — stale.
				return false
			}
			if writerTx != rd.WriterTx {
				// A different preceding tx is now the latest writer — stale.
				return false
			}
			if writerInc != rd.WriterIncarnation {
				// Same writer tx but different incarnation — value changed.
				return false
			}
		}
	}
	return true
}
