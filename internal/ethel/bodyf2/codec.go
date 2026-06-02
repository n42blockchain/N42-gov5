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

// ---- segment encode / decode (raw; the storage layer adds zstd) ----

// EncodeSegment serializes blocks to the F2 columnar layout, interning from/to
// addresses into dict. Senders must already be set on each F2Tx.From.
func EncodeSegment(blocks []F2Block, dict *AddrDict) []byte {
	var b []byte
	b = appendUvarint(b, uint64(len(blocks)))
	for bi := range blocks {
		blk := &blocks[bi]
		b = appendUvarint(b, uint64(len(blk.Txs)))
		b = appendUvarint(b, uint64(len(blk.Withdrawals)))
		for ti := range blk.Txs {
			tx := &blk.Txs[ti]
			b = append(b, tx.Type)
			b = appendUvarint(b, uint64(dict.Intern(tx.From)))
			if tx.To == nil {
				b = append(b, 0) // create
			} else {
				b = append(b, 1)
				b = appendUvarint(b, uint64(dict.Intern(*tx.To)))
			}
			b = appendUvarint(b, tx.Nonce)
			b = appendUvarint(b, tx.Gas)
			b = encValueSci(b, tx.Value)
			b = encValueSci(b, tx.GasFeeCap)
			if tx.Type >= 2 {
				b = encValueSci(b, tx.GasTipCap)
			}
			b = appendUvarint(b, uint64(len(tx.Data)))
			b = append(b, tx.Data...)
			b = appendUvarint(b, uint64(len(tx.Access)))
			for _, at := range tx.Access {
				b = append(b, at.Address[:]...)
				b = appendUvarint(b, uint64(len(at.StorageKeys)))
				for _, k := range at.StorageKeys {
					b = append(b, k[:]...)
				}
			}
		}
		for wi := range blk.Withdrawals {
			w := &blk.Withdrawals[wi]
			b = appendUvarint(b, w.Index)
			b = appendUvarint(b, w.Validator)
			b = append(b, w.Address[:]...)
			b = appendUvarint(b, w.Amount)
		}
	}
	return b
}

// DecodeSegment parses an F2 segment, resolving from/to IDs via dict.
func DecodeSegment(data []byte, dict *AddrDict) ([]F2Block, error) {
	r := &reader{b: data}
	nb, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	blocks := make([]F2Block, nb)
	for bi := uint64(0); bi < nb; bi++ {
		ntx, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		nwd, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		blk := &blocks[bi]
		blk.Txs = make([]F2Tx, ntx)
		for ti := uint64(0); ti < ntx; ti++ {
			tx := &blk.Txs[ti]
			if tx.Type, err = r.byte1(); err != nil {
				return nil, err
			}
			fid, err := r.uvarint()
			if err != nil {
				return nil, err
			}
			from, ok := dict.Addr(uint32(fid))
			if !ok {
				return nil, fmt.Errorf("bodyf2: from-ID %d out of dict", fid)
			}
			tx.From = from
			isTo, err := r.byte1()
			if err != nil {
				return nil, err
			}
			if isTo == 1 {
				tid, err := r.uvarint()
				if err != nil {
					return nil, err
				}
				to, ok := dict.Addr(uint32(tid))
				if !ok {
					return nil, fmt.Errorf("bodyf2: to-ID %d out of dict", tid)
				}
				tx.To = &to
			}
			if tx.Nonce, err = r.uvarint(); err != nil {
				return nil, err
			}
			if tx.Gas, err = r.uvarint(); err != nil {
				return nil, err
			}
			if tx.Value, err = r.valueSci(); err != nil {
				return nil, err
			}
			if tx.GasFeeCap, err = r.valueSci(); err != nil {
				return nil, err
			}
			if tx.Type >= 2 {
				if tx.GasTipCap, err = r.valueSci(); err != nil {
					return nil, err
				}
			}
			dlen, err := r.uvarint()
			if err != nil {
				return nil, err
			}
			d, err := r.bytes(int(dlen))
			if err != nil {
				return nil, err
			}
			tx.Data = append([]byte(nil), d...)
			nal, err := r.uvarint()
			if err != nil {
				return nil, err
			}
			for ai := uint64(0); ai < nal; ai++ {
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
		blk.Withdrawals = make([]F2Withdrawal, nwd)
		for wi := uint64(0); wi < nwd; wi++ {
			w := &blk.Withdrawals[wi]
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
