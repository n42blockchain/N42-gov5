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
	for i := range rw.Reads {
		if !readValid(mvs, rw.TxIndex, &rw.Reads[i]) {
			return false
		}
	}
	return true
}

// readValid reports whether one recorded read still holds against the store.
func readValid(mvs *MVS, txIndex int, rd *ReadDescriptor) bool {
	if rd.Key.Field == FieldBalance && rd.HasValue {
		return accountReadValid(mvs, txIndex, rd)
	}
	cur, writerTx, writerInc, found := mvs.Read(rd.Key, txIndex)
	if rd.FromBase {
		// Value was read from base DB. Still valid if no preceding tx
		// has since written this location, or if what it wrote is
		// byte-for-byte what the base held.
		if !found {
			return true
		}
		return rd.HasValue && bytes.Equal(cur, rd.Value)
	}
	// Value was read from MVS (written by tx[rd.WriterTx]).
	if !found {
		// The write we depended on was removed — stale.
		return false
	}
	if writerTx == rd.WriterTx && writerInc == rd.WriterIncarnation {
		return true
	}
	// A different writer, or the same writer re-executed. The read is
	// still valid if the bytes are the same: a sender's nonce chain does
	// not change because its predecessor re-ran over a recipient
	// conflict, and version-only validation cascaded that re-run down
	// every chain (round 35d, 64-wave limit at 15k transactions).
	return rd.HasValue && bytes.Equal(cur, rd.Value)
}

// accountReadValid validates an account read: the composition of the
// latest full write (or the base) with the deltas after it must be what the
// transaction saw -- on every field, or on every field but the balance
// when the read is balance-insensitive.
func accountReadValid(mvs *MVS, txIndex int, rd *ReadDescriptor) bool {
	full, fullTx, fullInc, found, delta := mvs.ReadAccount(rd.Key, txIndex)
	// Fast path: the same version as at read time, and no deltas then or now.
	if !rd.HadDelta && delta == nil {
		if rd.FromBase && !found {
			return true
		}
		if !rd.FromBase && found && fullTx == rd.WriterTx && fullInc == rd.WriterIncarnation {
			return true
		}
	}
	var cur []byte
	switch {
	case found:
		cur = composeAccount(full, delta)
	case rd.FromBase:
		cur = composeAccount(rd.Base, delta)
	default:
		return false // the full write it depended on was removed
	}
	if rd.IgnoreBalance {
		return accountEqualIgnoringBalance(cur, rd.Value)
	}
	return bytes.Equal(cur, rd.Value)
}
