// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// AggregateBatchTx (type 0x07) — aggregate-signature batch transaction (Feature
// B2). Unlike B1 (0x06, one signer authorizes the whole bundle), here N DIFFERENT
// senders each sign their OWN sub-tx with their BLS key, and the N signatures are
// aggregated into ONE 96-byte BLS signature (crypto/bls, BLS12-381). The batch
// thus authenticates many distinct users at one signature's footprint.
//
// BLS has no key recovery, so verification needs each sender's public key: senders
// register a BLS pubkey once on-chain (the pqregistry pattern) and the batch
// carries only the sender ADDRESS; Verify resolves address -> pubkey from state.
// Verify is a single AggregateVerify over the N distinct sub-tx hashes (O(N)
// pairings — a size win, not a CPU win; price per sub-tx). After verification the
// batch Expand()s into N ordinary DynamicFee txs (each its own sender + nonce).
//
// PoC scope: the type + per-sender sign / aggregate / aggregate-verify / expand /
// compact codec. The on-chain BLS pubkey registry and txpool/RLP wiring are the
// productionize follow-ups (docs/ethel/n42-architecture-and-custom-tx-plan.md).
package transaction

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

// AggregateBatchTxType is the transaction type byte for aggregate-signature batches.
const AggregateBatchTxType = 0x07

// AggregateBatchTx bundles N sub-txs from N distinct senders under one aggregate
// BLS signature. Sub-tx fields are columnar; each sender has its own nonce.
type AggregateBatchTx struct {
	ChainID   *uint256.Int
	GasTipCap *uint256.Int // shared
	GasFeeCap *uint256.Int // shared

	Sender []*types.Address // distinct per sub-tx; BLS pubkey resolved from registry
	Nonce  []uint64         // each sender's own nonce
	To     []*types.Address
	Value  []*uint256.Int
	Gas    []uint64
	Data   [][]byte

	AggSig []byte // 96-byte aggregated BLS signature over the N sub-tx hashes
}

// SubCount returns N.
func (tx *AggregateBatchTx) SubCount() int { return len(tx.Gas) }

// BLSAddress derives the account address for a BLS public key: keccak256(pub)[12:].
func BLSAddress(pub blscommon.PublicKey) types.Address {
	h := crypto.Keccak256(pub.Marshal())
	var a types.Address
	copy(a[:], h[12:])
	return a
}

// --- TxData interface (batch-level; sub-txs are produced by Expand) -----------

func (tx *AggregateBatchTx) txType() byte               { return AggregateBatchTxType }
func (tx *AggregateBatchTx) chainID() *uint256.Int      { return tx.ChainID }
func (tx *AggregateBatchTx) accessList() AccessList     { return nil }
func (tx *AggregateBatchTx) authList() AuthorizationList { return nil }
func (tx *AggregateBatchTx) data() []byte               { return nil }
func (tx *AggregateBatchTx) gasPrice() *uint256.Int     { return tx.GasFeeCap }
func (tx *AggregateBatchTx) gasTipCap() *uint256.Int    { return tx.GasTipCap }
func (tx *AggregateBatchTx) gasFeeCap() *uint256.Int    { return tx.GasFeeCap }
func (tx *AggregateBatchTx) to() *types.Address         { return nil }
func (tx *AggregateBatchTx) from() *types.Address       { return nil }
func (tx *AggregateBatchTx) sign() []byte               { return tx.AggSig }

func (tx *AggregateBatchTx) nonce() uint64 {
	if len(tx.Nonce) > 0 {
		return tx.Nonce[0]
	}
	return 0
}

func (tx *AggregateBatchTx) gas() uint64 {
	var g uint64
	for _, sg := range tx.Gas {
		g += sg
	}
	return g
}

func (tx *AggregateBatchTx) value() *uint256.Int {
	sum := new(uint256.Int)
	for _, v := range tx.Value {
		if v != nil {
			sum.Add(sum, v)
		}
	}
	return sum
}

func (tx *AggregateBatchTx) rawSignatureValues() (v, r, s *uint256.Int) { return nil, nil, nil }
func (tx *AggregateBatchTx) setSignatureValues(chainID, v, r, s *uint256.Int) {
	tx.ChainID = chainID
}
func (tx *AggregateBatchTx) hash() types.Hash { return crypto.Keccak256Hash(tx.encode(nil)) }

func (tx *AggregateBatchTx) copy() TxData {
	cpy := &AggregateBatchTx{
		ChainID:   copyU256(tx.ChainID),
		GasTipCap: copyU256(tx.GasTipCap),
		GasFeeCap: copyU256(tx.GasFeeCap),
		Sender:    make([]*types.Address, len(tx.Sender)),
		Nonce:     append([]uint64(nil), tx.Nonce...),
		To:        make([]*types.Address, len(tx.To)),
		Value:     make([]*uint256.Int, len(tx.Value)),
		Gas:       append([]uint64(nil), tx.Gas...),
		Data:      make([][]byte, len(tx.Data)),
		AggSig:    append([]byte(nil), tx.AggSig...),
	}
	for i := range tx.Sender {
		cpy.Sender[i] = copyAddressPtr(tx.Sender[i])
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

// --- per-sub-tx signing hash --------------------------------------------------

// SubHash is the message sub-tx i's sender signs (BLS) — the typed encoding of
// that sub-tx's fields plus the shared batch fields.
func (tx *AggregateBatchTx) SubHash(i int) types.Hash {
	b := make([]byte, 0, 96)
	b = append(b, AggregateBatchTxType)
	b = appendU256(b, tx.ChainID)
	b = appendU256(b, tx.GasTipCap)
	b = appendU256(b, tx.GasFeeCap)
	if tx.Sender[i] != nil {
		b = append(b, tx.Sender[i][:]...)
	}
	b = binary.AppendUvarint(b, tx.Nonce[i])
	if tx.To[i] != nil {
		b = append(b, 1)
		b = append(b, tx.To[i][:]...)
	} else {
		b = append(b, 0)
	}
	b = appendU256(b, tx.Value[i])
	b = binary.AppendUvarint(b, tx.Gas[i])
	b = binary.AppendUvarint(b, uint64(len(tx.Data[i])))
	b = append(b, tx.Data[i]...)
	return crypto.Keccak256Hash(b)
}

// AggregateBatchSign signs each sub-tx with its sender's BLS key (keys[i] must be
// sub-tx i's sender) and stores the aggregated signature. It also fills Sender[i]
// from each key so the batch is self-consistent.
func (tx *AggregateBatchTx) AggregateBatchSign(keys []blscommon.SecretKey) error {
	n := tx.SubCount()
	if len(keys) != n {
		return fmt.Errorf("agg batch: %d keys for %d sub-txs", len(keys), n)
	}
	if len(tx.Sender) != n {
		tx.Sender = make([]*types.Address, n)
	}
	sigs := make([]blscommon.Signature, n)
	for i := 0; i < n; i++ {
		a := BLSAddress(keys[i].PublicKey())
		tx.Sender[i] = &a
		sigs[i] = keys[i].Sign(tx.SubHash(i).Bytes())
	}
	tx.AggSig = bls.AggregateSignatures(sigs).Marshal()
	return nil
}

// Verify resolves each sender's BLS pubkey via resolve and checks the single
// aggregate signature against the N distinct sub-tx hashes.
func (tx *AggregateBatchTx) Verify(resolve func(types.Address) (blscommon.PublicKey, bool)) (bool, error) {
	n := tx.SubCount()
	if n == 0 {
		return false, fmt.Errorf("agg batch: empty")
	}
	if len(tx.Sender) != n || len(tx.Nonce) != n || len(tx.To) != n || len(tx.Value) != n || len(tx.Data) != n {
		return false, fmt.Errorf("agg batch: column length mismatch")
	}
	pubs := make([]blscommon.PublicKey, n)
	msgs := make([][32]byte, n)
	for i := 0; i < n; i++ {
		if tx.Sender[i] == nil {
			return false, fmt.Errorf("agg batch: nil sender %d", i)
		}
		pk, ok := resolve(*tx.Sender[i])
		if !ok {
			return false, fmt.Errorf("agg batch: no registered BLS key for %x", *tx.Sender[i])
		}
		pubs[i] = pk
		msgs[i] = tx.SubHash(i)
	}
	sig, err := bls.SignatureFromBytes(tx.AggSig)
	if err != nil {
		return false, err
	}
	return sig.AggregateVerify(pubs, msgs), nil
}

// Expand returns the N sub-txs as ordinary DynamicFee txs (each its own sender +
// nonce). The caller MUST have Verify()'d the aggregate signature first.
func (tx *AggregateBatchTx) Expand() ([]*Transaction, error) {
	n := tx.SubCount()
	if n == 0 {
		return nil, fmt.Errorf("agg batch: empty")
	}
	out := make([]*Transaction, n)
	for i := 0; i < n; i++ {
		sub := &DynamicFeeTx{
			ChainID:   copyU256(tx.ChainID),
			Nonce:     tx.Nonce[i],
			GasTipCap: copyU256(tx.GasTipCap),
			GasFeeCap: copyU256(tx.GasFeeCap),
			Gas:       tx.Gas[i],
			To:        copyAddressPtr(tx.To[i]),
			From:      copyAddressPtr(tx.Sender[i]), // authenticated by the aggregate sig
			Value:     copyU256(tx.Value[i]),
			Data:      types.CopyBytes(tx.Data[i]),
		}
		out[i] = NewTxOwned(sub)
	}
	return out, nil
}

// --- compact columnar codec ---------------------------------------------------

func (tx *AggregateBatchTx) encode(reg AddrRegistry) []byte {
	n := tx.SubCount()
	b := make([]byte, 0, 128+n*48)
	b = append(b, AggregateBatchTxType)
	b = appendU256(b, tx.ChainID)
	b = appendU256(b, tx.GasTipCap)
	b = appendU256(b, tx.GasFeeCap)
	b = binary.AppendUvarint(b, uint64(n))
	b = encodeToColumn(b, tx.Sender, reg) // senders (registry index / dict / raw)
	for _, nn := range tx.Nonce {
		b = binary.AppendUvarint(b, nn)
	}
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
	b = binary.AppendUvarint(b, uint64(len(tx.AggSig)))
	b = append(b, tx.AggSig...)
	return b
}

// EncodeBatch returns the compact wire bytes (raw/dict address columns).
func (tx *AggregateBatchTx) EncodeBatch() []byte { return tx.encode(nil) }

// EncodeBatchReg encodes with registry-index sender+to columns (item 2 compression).
func (tx *AggregateBatchTx) EncodeBatchReg(reg AddrRegistry) []byte { return tx.encode(reg) }

// DecodeAggregateBatch parses the compact wire form (no registry).
func DecodeAggregateBatch(data []byte) (*AggregateBatchTx, error) {
	return decodeAggregateBatch(data, nil)
}

// DecodeAggregateBatchReg parses the wire form, resolving registry-index columns.
func DecodeAggregateBatchReg(data []byte, reg AddrRegistry) (*AggregateBatchTx, error) {
	return decodeAggregateBatch(data, reg)
}

func decodeAggregateBatch(data []byte, reg AddrRegistry) (*AggregateBatchTx, error) {
	if len(data) == 0 || data[0] != AggregateBatchTxType {
		return nil, fmt.Errorf("agg batch: bad type byte")
	}
	p := 1
	tx := &AggregateBatchTx{}
	var err error
	if tx.ChainID, p, err = readU256(data, p); err != nil {
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
	if tx.Sender, p, err = decodeToColumn(data, p, n, reg); err != nil {
		return nil, err
	}
	tx.Nonce = make([]uint64, n)
	for i := 0; i < n; i++ {
		if tx.Nonce[i], p, err = readUvarint(data, p); err != nil {
			return nil, err
		}
	}
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
			return nil, fmt.Errorf("agg batch: truncated data")
		}
		tx.Data[i] = append([]byte{}, data[p:p+int(dl)]...)
		p += int(dl)
	}
	var sl uint64
	if sl, p, err = readUvarint(data, p); err != nil {
		return nil, err
	}
	if p+int(sl) > len(data) {
		return nil, fmt.Errorf("agg batch: truncated aggsig")
	}
	tx.AggSig = append([]byte{}, data[p:p+int(sl)]...)
	return tx, nil
}
