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

package transaction

import (
	"os"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// allocLean is N42_ALLOC_LEAN=1 (default off): EthBatchDecoder carves a
// legacy transfer out of a smaller slab entry (leanLegacyEntry). The
// decoded values, hash and encoding are those of the default path.
var allocLean atomic.Bool

func init() { allocLean.Store(os.Getenv("N42_ALLOC_LEAN") == "1") }

// AllocLean reports N42_ALLOC_LEAN.
func AllocLean() bool { return allocLean.Load() }

// SetAllocLeanForTest sets N42_ALLOC_LEAN's switch and returns the restore.
func SetAllocLeanForTest(on bool) func() {
	prev := allocLean.Swap(on)
	return func() { allocLean.Store(prev) }
}

// leanLegacyEntry is legacyEntry without its integers and recipient: 296
// bytes instead of 480. Under N42_ALLOC_LEAN a batch's legacy transactions
// keep
//   - their integers in the decoder's pointer-free uint256 slab (u), where
//     a gas price, value or V equal to the previous legacy transaction's
//     shares its slot: a transfer batch typically pays only R and S, 64
//     bytes, and
//   - their recipient as a pointer into the encoding, which the decoder
//     already aliases (the buffer is never modified while the transaction
//     lives).
//
// Sharing is safe because decoded integers are never modified in place:
// the accessors hand out the pointers, and a transaction's integers
// changing would already break its cached hash and encoding.
type leanLegacyEntry struct {
	tx Transaction
	t  LegacyTx
}

// leanLegacyU is the most uint256 slots one legacy transaction takes.
const leanLegacyU = 5

// leanLast is the previous lean legacy transaction's shareable integers;
// v2 is the V before the last different one (a sender's V alternates
// between its two recovery ids).
type leanLast struct {
	gasPrice, value, v, v2 *uint256.Int
}

func (d *EthBatchDecoder) nextLeanLegacy() *leanLegacyEntry {
	if d.nLL == len(d.ll) {
		d.ll = make([]leanLegacyEntry, d.slabSize(d.nLL))
		d.nLL = 0
	}
	return &d.ll[d.nLL]
}

// reserveU makes room for one legacy transaction's integers in the current
// u slab, so a transaction never straddles two slabs (a declined decode
// then only rewinds nU). A new slab holds what the rest of the batch is
// expected to take: two slots (R, S) per transaction for the first, the
// batch's observed slots per transaction after that.
func (d *EthBatchDecoder) reserveU() {
	if len(d.u)-d.nU >= leanLegacyU {
		return
	}
	d.uUsed += d.nU
	per := 2
	if d.nLeanTx > 0 {
		per = (d.uUsed + d.nLeanTx - 1) / d.nLeanTx
	}
	rest := d.left + 1 // the one being decoded included
	if rest < 1 {
		// Unexpectedly many: grow geometrically.
		rest = max(1, d.nLeanTx)
	}
	n := per*rest + 2*leanLegacyU
	d.u = make([]uint256.Int, n)
	d.nU = 0
}

// leanU decodes the next integer into the u slab; a value equal to *share
// reuses that slot, else *share becomes the new slot.
func (d *EthBatchDecoder) leanU(b []byte, share **uint256.Int) (*uint256.Int, []byte, bool) {
	z := &d.u[d.nU]
	rest, ok := fastUint256(z, b)
	if !ok {
		return nil, nil, false
	}
	if p := *share; p != nil && *p == *z {
		*z = uint256.Int{}
		return p, rest, true
	}
	*share = z
	d.nU++
	return z, rest, true
}

// leanV is leanU for V, shared with either of the last two V values.
func (d *EthBatchDecoder) leanV(b []byte) (*uint256.Int, []byte, bool) {
	z := &d.u[d.nU]
	rest, ok := fastUint256(z, b)
	if !ok {
		return nil, nil, false
	}
	l := &d.last
	if l.v != nil && *l.v == *z {
		*z = uint256.Int{}
		return l.v, rest, true
	}
	if l.v2 != nil && *l.v2 == *z {
		*z = uint256.Int{}
		l.v, l.v2 = l.v2, l.v
		return l.v, rest, true
	}
	l.v, l.v2 = z, l.v
	d.nU++
	return z, rest, true
}

// leanUnique decodes the next integer into its own u slot (R, S).
func (d *EthBatchDecoder) leanUnique(b []byte) (*uint256.Int, []byte, bool) {
	z := &d.u[d.nU]
	rest, ok := fastUint256(z, b)
	d.nU++
	return z, rest, ok
}

// fastToAliased is fastTo returning the recipient inside b.
func fastToAliased(b []byte) (*types.Address, []byte, bool) {
	single, c, rest, ok := fastString(b)
	if !ok || single {
		return nil, nil, false
	}
	switch len(c) {
	case 0:
		return nil, rest, true
	case types.AddressLength:
		return (*types.Address)(c), rest, true
	}
	return nil, nil, false
}

// decodeLeanLegacy is fastDecodeLegacyInto into a lean entry. On false the
// entry is partly written and the u slab and leanLast are rewound; the
// caller zeroes the entry.
func (d *EthBatchDecoder) decodeLeanLegacy(t *LegacyTx, data []byte) bool {
	d.reserveU()
	nU, last := d.nU, d.last
	if d.leanLegacyInto(t, data) {
		d.nLeanTx++
		return true
	}
	clear(d.u[nU:min(nU+leanLegacyU, len(d.u))])
	d.nU, d.last = nU, last
	return false
}

func (d *EthBatchDecoder) leanLegacyInto(t *LegacyTx, data []byte) bool {
	b, ok := fastOuterList(data)
	if !ok {
		return false
	}
	if t.Nonce, b, ok = fastUint64(b); !ok {
		return false
	}
	if t.GasPrice, b, ok = d.leanU(b, &d.last.gasPrice); !ok {
		return false
	}
	if t.Gas, b, ok = fastUint64(b); !ok {
		return false
	}
	if t.To, b, ok = fastToAliased(b); !ok {
		return false
	}
	if t.Value, b, ok = d.leanU(b, &d.last.value); !ok {
		return false
	}
	if t.Data, b, ok = fastBytes(b); !ok {
		return false
	}
	if t.V, b, ok = d.leanV(b); !ok {
		return false
	}
	if t.R, b, ok = d.leanUnique(b); !ok {
		return false
	}
	if t.S, b, ok = d.leanUnique(b); !ok {
		return false
	}
	return len(b) == 0
}
