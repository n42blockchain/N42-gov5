package transaction

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// Hand-rolled decoding of the two transfer shapes the fleet carries: legacy
// and EIP-1559 transactions with an empty access list (P9: frame decode was
// 6.7% of a node, 72% of it inside the reflection RLP decoder, and the
// reflection path allocates every uint256 and the address separately).
//
// The fast path accepts a strict subset of what rlp.DecodeBytes accepts and
// produces the identical value for it: anything it does not handle exactly
// -- non-canonical sizes or integers, overflow, a non-empty access list, a
// recipient that is neither empty nor 20 bytes, trailing bytes -- returns
// ok=false and the caller decodes with the reflection decoder, which then
// accepts or rejects it as before. TestFastDecodeMatchesReflection and
// FuzzFastDecode compare the two.

// reflectDecodeOnly turns the fast path off (tests compare the two).
var reflectDecodeOnly bool

// fastItem splits the canonical RLP item at the head of b. single reports a
// one-byte item (b[0] < 0x80, which is its own content).
func fastItem(b []byte) (list, single bool, content, rest []byte, ok bool) {
	if len(b) == 0 {
		return
	}
	h := b[0]
	switch {
	case h < 0x80:
		return false, true, b[:1], b[1:], true
	case h <= 0xb7:
		n := int(h - 0x80)
		if len(b) < 1+n {
			return
		}
		if n == 1 && b[1] < 0x80 {
			return // should have been a single byte
		}
		return false, false, b[1 : 1+n], b[1+n:], true
	case h < 0xc0:
		n, hl, good := fastLongSize(b, int(h-0xb7))
		if !good {
			return
		}
		return false, false, b[hl : hl+n], b[hl+n:], true
	case h <= 0xf7:
		n := int(h - 0xc0)
		if len(b) < 1+n {
			return
		}
		return true, false, b[1 : 1+n], b[1+n:], true
	default:
		n, hl, good := fastLongSize(b, int(h-0xf7))
		if !good {
			return
		}
		return true, false, b[hl : hl+n], b[hl+n:], true
	}
}

// fastLongSize reads a long-form size of ll bytes after b[0]: canonical (no
// leading zero, >= 56) and within b.
func fastLongSize(b []byte, ll int) (n, hl int, ok bool) {
	if ll > 4 || len(b) < 1+ll || b[1] == 0 {
		return 0, 0, false
	}
	for _, c := range b[1 : 1+ll] {
		n = n<<8 | int(c)
	}
	hl = 1 + ll
	if n < 56 || len(b)-hl < n {
		return 0, 0, false
	}
	return n, hl, true
}

// fastString takes the next string item (not a list).
func fastString(b []byte) (single bool, content, rest []byte, ok bool) {
	list, single, content, rest, ok := fastItem(b)
	if !ok || list {
		return false, nil, nil, false
	}
	return single, content, rest, true
}

// fastUint64 takes a canonical integer of at most 8 bytes.
func fastUint64(b []byte) (uint64, []byte, bool) {
	single, c, rest, ok := fastString(b)
	if !ok || len(c) > 8 || (len(c) > 0 && c[0] == 0) {
		return 0, nil, false // also rejects the single byte 0x00
	}
	_ = single
	var v uint64
	for _, x := range c {
		v = v<<8 | uint64(x)
	}
	return v, rest, true
}

// fastUint256 sets z to a canonical integer of at most 32 bytes.
func fastUint256(z *uint256.Int, b []byte) ([]byte, bool) {
	_, c, rest, ok := fastString(b)
	if !ok || len(c) > 32 || (len(c) > 0 && c[0] == 0) {
		return nil, false
	}
	z.SetBytes(c)
	return rest, true
}

// fastTo takes the recipient: empty (nil) or exactly 20 bytes.
func fastTo(dst *types.Address, b []byte) (*types.Address, []byte, bool) {
	single, c, rest, ok := fastString(b)
	if !ok || single {
		return nil, nil, false
	}
	switch len(c) {
	case 0:
		return nil, rest, true
	case types.AddressLength:
		copy(dst[:], c)
		return dst, rest, true
	}
	return nil, nil, false
}

// fastBytes takes a byte string as rlp's []byte decoder does: a fresh
// non-nil slice.
func fastBytes(b []byte) ([]byte, []byte, bool) {
	_, c, rest, ok := fastString(b)
	if !ok {
		return nil, nil, false
	}
	out := make([]byte, len(c))
	copy(out, c)
	return out, rest, true
}

// fastOuterList takes the transaction's list, which must span all of b.
func fastOuterList(b []byte) ([]byte, bool) {
	list, _, content, rest, ok := fastItem(b)
	if !ok || !list || len(rest) != 0 {
		return nil, false
	}
	return content, true
}

// legacyAlloc and dynamicFeeAlloc hold a decoded transaction's body, integers
// and recipient in one allocation.
type legacyAlloc struct {
	tx LegacyTx
	u  [5]uint256.Int
	to types.Address
}

type dynamicFeeAlloc struct {
	tx DynamicFeeTx
	u  [7]uint256.Int
	to types.Address
}

// fastDecodeLegacy decodes a legacy transaction list.
func fastDecodeLegacy(data []byte) (*Transaction, bool) {
	a := new(legacyAlloc)
	if !fastDecodeLegacyInto(a, data) {
		return nil, false
	}
	return NewTxOwned(&a.tx), true
}

// fastDecodeLegacyInto is fastDecodeLegacy into caller-provided, zeroed
// storage. On false a is partly written and must be zeroed before reuse.
func fastDecodeLegacyInto(a *legacyAlloc, data []byte) bool {
	b, ok := fastOuterList(data)
	if !ok {
		return false
	}
	t := &a.tx
	if t.Nonce, b, ok = fastUint64(b); !ok {
		return false
	}
	if b, ok = fastUint256(&a.u[0], b); !ok {
		return false
	}
	t.GasPrice = &a.u[0]
	if t.Gas, b, ok = fastUint64(b); !ok {
		return false
	}
	if t.To, b, ok = fastTo(&a.to, b); !ok {
		return false
	}
	if b, ok = fastUint256(&a.u[1], b); !ok {
		return false
	}
	t.Value = &a.u[1]
	if t.Data, b, ok = fastBytes(b); !ok {
		return false
	}
	for i, p := range []**uint256.Int{&t.V, &t.R, &t.S} {
		if b, ok = fastUint256(&a.u[2+i], b); !ok {
			return false
		}
		*p = &a.u[2+i]
	}
	return len(b) == 0
}

// fastDecodeDynamicFee decodes an EIP-1559 payload (after the type byte)
// whose access list is empty.
func fastDecodeDynamicFee(data []byte) (*Transaction, bool) {
	a := new(dynamicFeeAlloc)
	if !fastDecodeDynamicFeeInto(a, data) {
		return nil, false
	}
	return NewTxOwned(&a.tx), true
}

// fastDecodeDynamicFeeInto is fastDecodeDynamicFee into caller-provided,
// zeroed storage. On false a is partly written and must be zeroed before
// reuse.
func fastDecodeDynamicFeeInto(a *dynamicFeeAlloc, data []byte) bool {
	b, ok := fastOuterList(data)
	if !ok {
		return false
	}
	t := &a.tx
	if b, ok = fastUint256(&a.u[0], b); !ok {
		return false
	}
	t.ChainID = &a.u[0]
	if t.Nonce, b, ok = fastUint64(b); !ok {
		return false
	}
	if b, ok = fastUint256(&a.u[1], b); !ok {
		return false
	}
	t.GasTipCap = &a.u[1]
	if b, ok = fastUint256(&a.u[2], b); !ok {
		return false
	}
	t.GasFeeCap = &a.u[2]
	if t.Gas, b, ok = fastUint64(b); !ok {
		return false
	}
	if t.To, b, ok = fastTo(&a.to, b); !ok {
		return false
	}
	if b, ok = fastUint256(&a.u[3], b); !ok {
		return false
	}
	t.Value = &a.u[3]
	if t.Data, b, ok = fastBytes(b); !ok {
		return false
	}
	// Only the empty access list: rlp decodes it to a non-nil empty slice.
	if len(b) == 0 || b[0] != 0xc0 {
		return false
	}
	b = b[1:]
	t.AccessList = AccessList{}
	for i, p := range []**uint256.Int{&t.V, &t.R, &t.S} {
		if b, ok = fastUint256(&a.u[4+i], b); !ok {
			return false
		}
		*p = &a.u[4+i]
	}
	return len(b) == 0
}
