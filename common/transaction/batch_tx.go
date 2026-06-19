// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// CompressedBatchTx (type 0x06) — single-signature batch transaction (Feature B1).
//
// One signer authorizes N sub-transactions, all FROM that signer, with
// SEQUENTIAL nonces [BaseNonce, BaseNonce+N-1], under ONE secp256k1 ECDSA
// signature over the whole bundle. The signer is recovered once (ecrecover) and
// every sub-tx inherits it — so per-sub-tx the signature, the sender, and the
// nonce are all implicit. Combined with columnar field packing this approaches
// the ~12 B/tx ledger-field target for transfer-heavy batches (e.g. an exchange
// or payment processor submitting thousands of its own operations).
//
// At acceptance the batch is verified once (one ecrecover) and Expand()ed into N
// ordinary DynamicFee-shaped transactions with From pre-set, which the existing
// txpool / executor consume unchanged (the batch container is never executed
// directly). This is the PoC of the type + its sign/recover/expand/compression;
// the txpool accept-and-expand wiring and protobuf/RLP-wire dispatch are the
// productionize follow-ups (see docs/ethel/n42-architecture-and-custom-tx-plan.md).
package transaction

import (
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// CompressedBatchTxType is the transaction type byte for single-signature batches.
const CompressedBatchTxType = 0x06

// CompressedBatchTx is a bundle of N sub-txs from one signer (sequential nonces)
// under one ECDSA signature. The sub-tx fields are stored columnar.
type CompressedBatchTx struct {
	ChainID   *uint256.Int
	BaseNonce uint64       // sub-tx i has nonce BaseNonce+i
	GasTipCap *uint256.Int // shared across sub-txs
	GasFeeCap *uint256.Int // shared across sub-txs

	// Columnar sub-tx fields, all length N.
	To    []*types.Address // nil entry => contract creation
	Value []*uint256.Int
	Gas   []uint64
	Data  [][]byte

	// One signature over SigningHash() (covers all sub-txs).
	V, R, S *uint256.Int
}

// SubCount returns N, the number of sub-transactions.
func (tx *CompressedBatchTx) SubCount() int { return len(tx.Gas) }

// --- TxData interface ---------------------------------------------------------

func (tx *CompressedBatchTx) txType() byte           { return CompressedBatchTxType }
func (tx *CompressedBatchTx) chainID() *uint256.Int  { return tx.ChainID }
func (tx *CompressedBatchTx) accessList() AccessList  { return nil }
func (tx *CompressedBatchTx) authList() AuthorizationList { return nil }
func (tx *CompressedBatchTx) data() []byte            { return nil } // no single calldata
func (tx *CompressedBatchTx) gasPrice() *uint256.Int  { return tx.GasFeeCap }
func (tx *CompressedBatchTx) gasTipCap() *uint256.Int { return tx.GasTipCap }
func (tx *CompressedBatchTx) gasFeeCap() *uint256.Int { return tx.GasFeeCap }
func (tx *CompressedBatchTx) nonce() uint64           { return tx.BaseNonce }
func (tx *CompressedBatchTx) to() *types.Address      { return nil } // no single recipient
func (tx *CompressedBatchTx) from() *types.Address    { return nil } // recovered from signature
func (tx *CompressedBatchTx) sign() []byte            { return nil }

// gas returns the TOTAL gas of the batch (sum of sub-tx gas) — its block-gas
// footprint and the basis for the batch's cost/balance check.
func (tx *CompressedBatchTx) gas() uint64 {
	var g uint64
	for _, sg := range tx.Gas {
		g += sg
	}
	return g
}

// value returns the TOTAL value moved by the batch (sum of sub-tx values).
func (tx *CompressedBatchTx) value() *uint256.Int {
	sum := new(uint256.Int)
	for _, v := range tx.Value {
		if v != nil {
			sum.Add(sum, v)
		}
	}
	return sum
}

func (tx *CompressedBatchTx) rawSignatureValues() (v, r, s *uint256.Int) {
	return tx.V, tx.R, tx.S
}

func (tx *CompressedBatchTx) setSignatureValues(chainID, v, r, s *uint256.Int) {
	tx.ChainID, tx.V, tx.R, tx.S = chainID, v, r, s
}

func (tx *CompressedBatchTx) hash() types.Hash {
	return crypto.Keccak256Hash(tx.encode(true, nil))
}

func (tx *CompressedBatchTx) copy() TxData {
	cpy := &CompressedBatchTx{
		ChainID:   copyU256(tx.ChainID),
		BaseNonce: tx.BaseNonce,
		GasTipCap: copyU256(tx.GasTipCap),
		GasFeeCap: copyU256(tx.GasFeeCap),
		To:        make([]*types.Address, len(tx.To)),
		Value:     make([]*uint256.Int, len(tx.Value)),
		Gas:       append([]uint64(nil), tx.Gas...),
		Data:      make([][]byte, len(tx.Data)),
		V:         copyU256(tx.V),
		R:         copyU256(tx.R),
		S:         copyU256(tx.S),
	}
	for i := range tx.To {
		cpy.To[i] = copyAddressPtr(tx.To[i])
	}
	for i := range tx.Value {
		cpy.Value[i] = copyU256(tx.Value[i])
	}
	for i := range tx.Data {
		cpy.Data[i] = types.CopyBytes(tx.Data[i])
	}
	return cpy
}

// --- signing / recovery -------------------------------------------------------

// SigningHash is the message signed by the single ECDSA signature: the typed
// (0x06-prefixed) encoding of every field EXCEPT V/R/S. Always uses reg=nil so it
// is registry-INDEPENDENT — re-encoding the wire with registry indices does not
// change what was signed (decode reconstructs the addresses, recover recomputes
// this canonically).
func (tx *CompressedBatchTx) SigningHash() types.Hash {
	return crypto.Keccak256Hash(tx.encode(false, nil))
}

// Sign signs the batch with one secp256k1 key and fills V/R/S. The signer becomes
// the sender of every sub-tx.
func (tx *CompressedBatchTx) Sign(prv *ecdsa.PrivateKey) error {
	sig, err := crypto.Sign(tx.SigningHash().Bytes(), prv)
	if err != nil {
		return err
	}
	tx.R = new(uint256.Int).SetBytes(sig[:32])
	tx.S = new(uint256.Int).SetBytes(sig[32:64])
	tx.V = new(uint256.Int).SetBytes([]byte{sig[64]})
	return nil
}

// RecoverSender recovers the single signer from the batch signature.
func (tx *CompressedBatchTx) RecoverSender() (types.Address, error) {
	if tx.R == nil || tx.S == nil || tx.V == nil {
		return types.Address{}, fmt.Errorf("batch tx: missing signature")
	}
	sig := make([]byte, 65)
	tx.R.WriteToSlice(sig[:32])
	tx.S.WriteToSlice(sig[32:64])
	sig[64] = byte(tx.V.Uint64())
	pub, err := crypto.SigToPub(tx.SigningHash().Bytes(), sig)
	if err != nil {
		return types.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// Expand verifies the batch signature once and returns the N sub-transactions as
// ordinary DynamicFee txs with From pre-set to the recovered signer and
// sequential nonces. These are what the txpool/executor process; the batch
// itself is never executed.
func (tx *CompressedBatchTx) Expand() ([]*Transaction, error) {
	n := tx.SubCount()
	if n == 0 {
		return nil, fmt.Errorf("batch tx: empty")
	}
	if len(tx.To) != n || len(tx.Value) != n || len(tx.Data) != n {
		return nil, fmt.Errorf("batch tx: column length mismatch")
	}
	from, err := tx.RecoverSender()
	if err != nil {
		return nil, err
	}
	out := make([]*Transaction, n)
	for i := 0; i < n; i++ {
		fromCopy := from
		sub := &DynamicFeeTx{
			ChainID:   copyU256(tx.ChainID),
			Nonce:     tx.BaseNonce + uint64(i),
			GasTipCap: copyU256(tx.GasTipCap),
			GasFeeCap: copyU256(tx.GasFeeCap),
			Gas:       tx.Gas[i],
			To:        copyAddressPtr(tx.To[i]),
			From:      &fromCopy, // batch sig already authorized => no per-sub sig
			Value:     copyU256(tx.Value[i]),
			Data:      types.CopyBytes(tx.Data[i]),
		}
		out[i] = NewTxOwned(sub)
	}
	return out, nil
}

// --- compact columnar codec ---------------------------------------------------

// encode produces the compact wire form: 0x06 || header || columns [|| sig].
func (tx *CompressedBatchTx) encode(withSig bool, reg AddrRegistry) []byte {
	n := tx.SubCount()
	b := make([]byte, 0, 64+n*32)
	b = append(b, CompressedBatchTxType)
	b = appendU256(b, tx.ChainID)
	b = binary.AppendUvarint(b, tx.BaseNonce)
	b = appendU256(b, tx.GasTipCap)
	b = appendU256(b, tx.GasFeeCap)
	b = binary.AppendUvarint(b, uint64(n))
	b = encodeToColumn(b, tx.To, reg)
	for _, v := range tx.Value {
		b = appendU256(b, v)
	}
	for _, g := range tx.Gas {
		b = binary.AppendUvarint(b, g)
	}
	for _, d := range tx.Data {
		b = binary.AppendUvarint(b, uint64(len(d)))
		b = append(b, d...)
	}
	if withSig {
		b = appendU256(b, tx.R)
		b = appendU256(b, tx.S)
		b = appendU256(b, tx.V)
	}
	return b
}

// EncodeBatch returns the signed compact wire bytes (0x06 || ... || sig), with
// raw/dict address columns (no registry).
func (tx *CompressedBatchTx) EncodeBatch() []byte { return tx.encode(true, nil) }

// EncodeBatchReg encodes with registry-index address columns when recipients are
// registered (item 2 compression) — far smaller for all-distinct recipients.
func (tx *CompressedBatchTx) EncodeBatchReg(reg AddrRegistry) []byte { return tx.encode(true, reg) }

// DecodeBatch parses the compact wire form (no registry; fails on a registry-index column).
func DecodeBatch(data []byte) (*CompressedBatchTx, error) { return decodeBatch(data, nil) }

// DecodeBatchReg parses the compact wire form, resolving registry-index columns via reg.
func DecodeBatchReg(data []byte, reg AddrRegistry) (*CompressedBatchTx, error) {
	return decodeBatch(data, reg)
}

func decodeBatch(data []byte, reg AddrRegistry) (*CompressedBatchTx, error) {
	if len(data) == 0 || data[0] != CompressedBatchTxType {
		return nil, fmt.Errorf("batch tx: bad type byte")
	}
	p := 1
	tx := &CompressedBatchTx{}
	var err error
	if tx.ChainID, p, err = readU256(data, p); err != nil {
		return nil, err
	}
	if tx.BaseNonce, p, err = readUvarint(data, p); err != nil {
		return nil, err
	}
	if tx.GasTipCap, p, err = readU256(data, p); err != nil {
		return nil, err
	}
	if tx.GasFeeCap, p, err = readU256(data, p); err != nil {
		return nil, err
	}
	var nn uint64
	if nn, p, err = readUvarint(data, p); err != nil {
		return nil, err
	}
	n := int(nn)
	if tx.To, p, err = decodeToColumn(data, p, n, reg); err != nil {
		return nil, err
	}
	tx.Value = make([]*uint256.Int, n)
	for i := 0; i < n; i++ {
		if tx.Value[i], p, err = readU256(data, p); err != nil {
			return nil, err
		}
	}
	tx.Gas = make([]uint64, n)
	for i := 0; i < n; i++ {
		if tx.Gas[i], p, err = readUvarint(data, p); err != nil {
			return nil, err
		}
	}
	tx.Data = make([][]byte, n)
	for i := 0; i < n; i++ {
		var dl uint64
		if dl, p, err = readUvarint(data, p); err != nil {
			return nil, err
		}
		if p+int(dl) > len(data) {
			return nil, fmt.Errorf("batch tx: truncated data")
		}
		tx.Data[i] = append([]byte{}, data[p:p+int(dl)]...)
		p += int(dl)
	}
	if tx.R, p, err = readU256(data, p); err != nil {
		return nil, err
	}
	if tx.S, p, err = readU256(data, p); err != nil {
		return nil, err
	}
	if tx.V, p, err = readU256(data, p); err != nil {
		return nil, err
	}
	return tx, nil
}

// encodeToColumn writes the To column with an adaptive scheme: mode 0 = raw
// (1 present-flag byte + 20 B addr each), mode 1 = dictionary (distinct addrs
// once + a varint index per sub-tx, 0 = nil/creation). Dict is chosen when
// recipients repeat enough to beat raw — the lever toward ~12 B/tx for batches
// that pay a bounded recipient set; all-distinct recipients stay on raw.
func encodeToColumn(b []byte, to []*types.Address, reg AddrRegistry) []byte {
	// Mode 2 (registry index): if every non-nil address is registered on-chain,
	// reference each by its compact registry index (varint, 0 = nil) — no addr
	// bytes in the tx at all. Beats raw/dict even for all-distinct recipients.
	if allRegistered(to, reg) {
		b = append(b, 2)
		for _, a := range to {
			if a == nil {
				b = binary.AppendUvarint(b, 0)
			} else {
				ix, _ := reg.IndexOf(*a)
				b = binary.AppendUvarint(b, ix)
			}
		}
		return b
	}
	n := len(to)
	idx := make(map[types.Address]int, n) // addr -> 1-based dict index
	var dict []types.Address
	for _, a := range to {
		if a == nil {
			continue
		}
		if _, ok := idx[*a]; !ok {
			dict = append(dict, *a)
			idx[*a] = len(dict)
		}
	}
	// Dict pays off only with reuse: raw ≈ n*(1+20); dict ≈ 1+varint(D)+D*20+n*~2.
	useDict := len(dict) < n && len(dict)*20+n*2 < n*21
	if !useDict {
		b = append(b, 0)
		for _, a := range to {
			if a == nil {
				b = append(b, 0)
			} else {
				b = append(b, 1)
				b = append(b, a[:]...)
			}
		}
		return b
	}
	b = append(b, 1)
	b = binary.AppendUvarint(b, uint64(len(dict)))
	for i := range dict {
		b = append(b, dict[i][:]...)
	}
	for _, a := range to {
		if a == nil {
			b = binary.AppendUvarint(b, 0)
		} else {
			b = binary.AppendUvarint(b, uint64(idx[*a]))
		}
	}
	return b
}

func decodeToColumn(data []byte, p, n int, reg AddrRegistry) ([]*types.Address, int, error) {
	if p >= len(data) {
		return nil, p, fmt.Errorf("batch tx: truncated to column")
	}
	mode := data[p]
	p++
	to := make([]*types.Address, n)
	switch mode {
	case 2:
		if reg == nil {
			return nil, p, fmt.Errorf("batch tx: registry-index column but no registry")
		}
		for i := 0; i < n; i++ {
			ix, m := binary.Uvarint(data[p:])
			if m <= 0 {
				return nil, p, fmt.Errorf("batch tx: bad registry index")
			}
			p += m
			if ix > 0 {
				a, ok := reg.AddrAt(ix)
				if !ok {
					return nil, p, fmt.Errorf("batch tx: registry index %d unknown", ix)
				}
				ac := a
				to[i] = &ac
			}
		}
	case 0:
		for i := 0; i < n; i++ {
			if p >= len(data) {
				return nil, p, fmt.Errorf("batch tx: truncated to[]")
			}
			flag := data[p]
			p++
			if flag == 1 {
				if p+20 > len(data) {
					return nil, p, fmt.Errorf("batch tx: truncated addr")
				}
				var a types.Address
				copy(a[:], data[p:p+20])
				to[i] = &a
				p += 20
			}
		}
	case 1:
		d, m := binary.Uvarint(data[p:])
		if m <= 0 {
			return nil, p, fmt.Errorf("batch tx: bad dict len")
		}
		p += m
		dict := make([]types.Address, d)
		for i := 0; i < int(d); i++ {
			if p+20 > len(data) {
				return nil, p, fmt.Errorf("batch tx: truncated dict addr")
			}
			copy(dict[i][:], data[p:p+20])
			p += 20
		}
		for i := 0; i < n; i++ {
			ix, m := binary.Uvarint(data[p:])
			if m <= 0 {
				return nil, p, fmt.Errorf("batch tx: bad to index")
			}
			p += m
			if ix > 0 {
				if int(ix) > len(dict) {
					return nil, p, fmt.Errorf("batch tx: to index out of range")
				}
				a := dict[ix-1]
				to[i] = &a
			}
		}
	default:
		return nil, p, fmt.Errorf("batch tx: bad to mode %d", mode)
	}
	return to, p, nil
}

// --- small codec helpers ------------------------------------------------------

// readU256 mirrors appendU256 (tx_compact.go): 1 length byte then minimal
// big-endian bytes; 0xFF length = nil pointer.
func readU256(data []byte, p int) (*uint256.Int, int, error) {
	if p >= len(data) {
		return nil, p, fmt.Errorf("batch tx: truncated u256 len")
	}
	l := int(data[p])
	p++
	if l == 0xFF {
		return nil, p, nil
	}
	if l > 32 || p+l > len(data) {
		return nil, p, fmt.Errorf("batch tx: bad u256 len %d", l)
	}
	v := new(uint256.Int).SetBytes(data[p : p+l])
	return v, p + l, nil
}

func readUvarint(data []byte, p int) (uint64, int, error) {
	v, m := binary.Uvarint(data[p:])
	if m <= 0 {
		return 0, p, fmt.Errorf("batch tx: bad uvarint")
	}
	return v, p + m, nil
}

func copyU256(v *uint256.Int) *uint256.Int {
	if v == nil {
		return nil
	}
	return new(uint256.Int).Set(v)
}
