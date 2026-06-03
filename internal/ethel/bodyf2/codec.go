package bodyf2

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// F2AccessTuple is one EIP-2930 access-list entry.
type F2AccessTuple struct {
	Address     types.Address
	StorageKeys [][32]byte
}

// F2Withdrawal mirrors a post-Shanghai withdrawal (ledger fields).
type F2Withdrawal struct {
	Index, Validator uint64
	Address          types.Address
	Amount           uint64
}

// F2Tx is the trust-history (no-signature) transaction: full ledger content,
// From resolved from the dict (no ecrecover), no R/S/V, no canonical hash.
// NOTE: blob hashes (type 3) and 7702 auth lists (type 4) are not yet carried —
// F2 is ledger-faithful, not wire-faithful; add them if those fields must be
// served. Type is preserved so consumers know the tx kind.
type F2Tx struct {
	Type      uint8
	From      types.Address
	To        *types.Address // nil = contract creation
	Nonce     uint64
	Gas       uint64
	Value     *uint256.Int
	GasFeeCap *uint256.Int // legacy/dynamic: the gas price / fee cap
	GasTipCap *uint256.Int // type>=2 only
	Data      []byte
	Access    []F2AccessTuple
}

// F2Block is a decoded block body in F2 form.
type F2Block struct {
	Txs         []F2Tx
	Withdrawals []F2Withdrawal
}

// ---- varint / value helpers ----

func appendUvarint(b []byte, v uint64) []byte {
	var t [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(t[:], v)
	return append(b, t[:n]...)
}

type reader struct {
	b   []byte
	pos int
}

func (r *reader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.b[r.pos:])
	if n <= 0 {
		return 0, fmt.Errorf("bodyf2: bad varint at %d", r.pos)
	}
	r.pos += n
	return v, nil
}
func (r *reader) bytes(n int) ([]byte, error) {
	if r.pos+n > len(r.b) {
		return nil, fmt.Errorf("bodyf2: short read %d at %d", n, r.pos)
	}
	out := r.b[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}
func (r *reader) byte1() (byte, error) {
	if r.pos >= len(r.b) {
		return 0, fmt.Errorf("bodyf2: eof")
	}
	c := r.b[r.pos]
	r.pos++
	return c, nil
}

var big10u = uint256.NewInt(10)

// encValueSci encodes a uint256 as mantissa×10^exp (financial/scientific
// notation): ctrl byte = exp (0..127); then the mantissa as [len][big-endian].
// Trailing decimal zeros are stripped so round amounts shrink to a few bytes.
func encValueSci(b []byte, v *uint256.Int) []byte {
	if v == nil || v.IsZero() {
		return append(b, 0, 0) // exp=0, mantissa len=0
	}
	m := new(uint256.Int).Set(v)
	exp := byte(0)
	q := new(uint256.Int)
	r := new(uint256.Int)
	for exp < 127 {
		q.DivMod(m, big10u, r)
		if !r.IsZero() {
			break
		}
		m.Set(q)
		exp++
	}
	mb := m.Bytes() // big-endian, trimmed
	b = append(b, exp)
	b = append(b, byte(len(mb)))
	return append(b, mb...)
}

func (r *reader) valueSci() (*uint256.Int, error) {
	exp, err := r.byte1()
	if err != nil {
		return nil, err
	}
	mlen, err := r.byte1()
	if err != nil {
		return nil, err
	}
	mb, err := r.bytes(int(mlen))
	if err != nil {
		return nil, err
	}
	v := new(uint256.Int).SetBytes(mb)
	for i := byte(0); i < exp; i++ {
		v.Mul(v, big10u)
	}
	return v, nil
}

// ---- bit helpers ----

func appendBits(b []byte, bits []bool) []byte {
	packed := make([]byte, (len(bits)+7)/8)
	for i, bit := range bits {
		if bit {
			packed[i/8] |= 1 << (uint(i) % 8)
		}
	}
	return append(b, packed...)
}

func (r *reader) bits(n int) ([]bool, error) {
	buf, err := r.bytes((n + 7) / 8)
	if err != nil {
		return nil, err
	}
	bits := make([]bool, n)
	for i := 0; i < n; i++ {
		bits[i] = buf[i/8]&(1<<(uint(i)%8)) != 0
	}
	return bits, nil
}

// ---- segment encode / decode (raw; the storage layer adds zstd) ----
//
// COLUMN-MAJOR layout: each field is grouped across ALL txs in the segment
// (all types, then all from-IDs, ...) so adjacent bytes are similar and zstd
// compresses far better than a per-tx interleaving. Conditional columns (to-ID
// for non-creates, tip for type>=2) follow the type/isCreate columns that drive
// them, so the decoder knows the per-tx shape before reading them.

// EncodeSegment serializes blocks to the F2 column-major layout, interning
// from/to addresses into dict. Senders must already be set on each F2Tx.From.
func EncodeSegment(blocks []F2Block, dict *AddrDict) []byte {
	var txs []*F2Tx
	var b []byte
	b = appendUvarint(b, uint64(len(blocks)))
	for bi := range blocks {
		b = appendUvarint(b, uint64(len(blocks[bi].Txs)))
		b = appendUvarint(b, uint64(len(blocks[bi].Withdrawals)))
		for ti := range blocks[bi].Txs {
			txs = append(txs, &blocks[bi].Txs[ti])
		}
	}

	// type column
	for _, tx := range txs {
		b = append(b, tx.Type)
	}
	// isCreate bitpack
	creates := make([]bool, len(txs))
	for i, tx := range txs {
		creates[i] = tx.To == nil
	}
	b = appendBits(b, creates)
	// from-ID column
	for _, tx := range txs {
		b = appendUvarint(b, uint64(dict.Intern(tx.From)))
	}
	// to-ID column (non-create only)
	for _, tx := range txs {
		if tx.To != nil {
			b = appendUvarint(b, uint64(dict.Intern(*tx.To)))
		}
	}
	// nonce, gas
	for _, tx := range txs {
		b = appendUvarint(b, tx.Nonce)
	}
	for _, tx := range txs {
		b = appendUvarint(b, tx.Gas)
	}
	// value, gasFeeCap
	for _, tx := range txs {
		b = encValueSci(b, tx.Value)
	}
	for _, tx := range txs {
		b = encValueSci(b, tx.GasFeeCap)
	}
	// gasTipCap (type>=2 only)
	for _, tx := range txs {
		if tx.Type >= 2 {
			b = encValueSci(b, tx.GasTipCap)
		}
	}
	// calldata: lengths, then concatenated bytes
	for _, tx := range txs {
		b = appendUvarint(b, uint64(len(tx.Data)))
	}
	for _, tx := range txs {
		b = append(b, tx.Data...)
	}
	// accessList: per-tx tuple count, then tuples
	for _, tx := range txs {
		b = appendUvarint(b, uint64(len(tx.Access)))
	}
	for _, tx := range txs {
		for _, at := range tx.Access {
			b = append(b, at.Address[:]...)
			b = appendUvarint(b, uint64(len(at.StorageKeys)))
			for _, k := range at.StorageKeys {
				b = append(b, k[:]...)
			}
		}
	}
	// withdrawals per block
	for bi := range blocks {
		for wi := range blocks[bi].Withdrawals {
			w := &blocks[bi].Withdrawals[wi]
			b = appendUvarint(b, w.Index)
			b = appendUvarint(b, w.Validator)
			b = append(b, w.Address[:]...)
			b = appendUvarint(b, w.Amount)
		}
	}
	return b
}

// DecodeSegment parses a column-major F2 segment, resolving from/to IDs via dict.
func DecodeSegment(data []byte, dict *AddrDict) ([]F2Block, error) {
	r := &reader{b: data}
	nb, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	blocks := make([]F2Block, nb)
	wdCounts := make([]int, nb)
	var txs []*F2Tx
	for bi := uint64(0); bi < nb; bi++ {
		ntx, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		nwd, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		blocks[bi].Txs = make([]F2Tx, ntx)
		blocks[bi].Withdrawals = make([]F2Withdrawal, nwd)
		wdCounts[bi] = int(nwd)
		for ti := range blocks[bi].Txs {
			txs = append(txs, &blocks[bi].Txs[ti])
		}
	}

	// type
	for _, tx := range txs {
		if tx.Type, err = r.byte1(); err != nil {
			return nil, err
		}
	}
	// isCreate
	creates, err := r.bits(len(txs))
	if err != nil {
		return nil, err
	}
	// from-ID
	for _, tx := range txs {
		id, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		from, ok := dict.Addr(uint32(id))
		if !ok {
			return nil, fmt.Errorf("bodyf2: from-ID %d out of dict", id)
		}
		tx.From = from
	}
	// to-ID (non-create)
	for i, tx := range txs {
		if creates[i] {
			continue
		}
		id, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		to, ok := dict.Addr(uint32(id))
		if !ok {
			return nil, fmt.Errorf("bodyf2: to-ID %d out of dict", id)
		}
		tx.To = &to
	}
	// nonce, gas
	for _, tx := range txs {
		if tx.Nonce, err = r.uvarint(); err != nil {
			return nil, err
		}
	}
	for _, tx := range txs {
		if tx.Gas, err = r.uvarint(); err != nil {
			return nil, err
		}
	}
	// value, gasFeeCap
	for _, tx := range txs {
		if tx.Value, err = r.valueSci(); err != nil {
			return nil, err
		}
	}
	for _, tx := range txs {
		if tx.GasFeeCap, err = r.valueSci(); err != nil {
			return nil, err
		}
	}
	// gasTipCap (type>=2)
	for _, tx := range txs {
		if tx.Type >= 2 {
			if tx.GasTipCap, err = r.valueSci(); err != nil {
				return nil, err
			}
		}
	}
	// calldata lengths, then bytes
	dlens := make([]int, len(txs))
	for i := range txs {
		l, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		dlens[i] = int(l)
	}
	for i, tx := range txs {
		d, err := r.bytes(dlens[i])
		if err != nil {
			return nil, err
		}
		tx.Data = append([]byte(nil), d...)
	}
	// accessList counts, then tuples
	alens := make([]int, len(txs))
	for i := range txs {
		l, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		alens[i] = int(l)
	}
	for i, tx := range txs {
		for ai := 0; ai < alens[i]; ai++ {
			var at F2AccessTuple
			ab, err := r.bytes(20)
			if err != nil {
				return nil, err
			}
			copy(at.Address[:], ab)
			nk, err := r.uvarint()
			if err != nil {
				return nil, err
			}
			at.StorageKeys = make([][32]byte, nk)
			for ki := uint64(0); ki < nk; ki++ {
				kb, err := r.bytes(32)
				if err != nil {
					return nil, err
				}
				copy(at.StorageKeys[ki][:], kb)
			}
			tx.Access = append(tx.Access, at)
		}
	}
	// withdrawals per block
	for bi := range blocks {
		for wi := range blocks[bi].Withdrawals {
			w := &blocks[bi].Withdrawals[wi]
			if w.Index, err = r.uvarint(); err != nil {
				return nil, err
			}
			if w.Validator, err = r.uvarint(); err != nil {
				return nil, err
			}
			ab, err := r.bytes(20)
			if err != nil {
				return nil, err
			}
			copy(w.Address[:], ab)
			if w.Amount, err = r.uvarint(); err != nil {
				return nil, err
			}
		}
	}
	return blocks, nil
}
