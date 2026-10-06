// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// sampleTxsOneOfEach builds one signed-looking transaction of each type
// (structurally valid V/R/S, not a real ECDSA signature -- this package
// tests decode/reuse behavior, not signature verification), round-tripped
// through the exact Ethereum wire encoding a block stores
// (transaction.EncodeEthereumTransaction, the same call EthEncoded/
// blockRLP.TxData uses), mirroring common/transaction's own
// TestEthereumTransactionRoundTrip fixture.
func sampleTxsOneOfEach(t *testing.T) []*transaction.Transaction {
	t.Helper()
	to := types.HexToAddress("0x1111111111111111111111111111111111111111")
	blobTo := types.HexToAddress("0x2222222222222222222222222222222222222222")
	delegateTo := types.HexToAddress("0x3333333333333333333333333333333333333333")
	storageKey := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	blobHash := types.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000001")

	raw := []*transaction.Transaction{
		transaction.NewTx(&transaction.LegacyTx{
			Nonce: 1, GasPrice: uint256.NewInt(10), Gas: 21000, To: &to,
			Value: uint256.NewInt(20), Data: []byte{0x01, 0x02},
			V: uint256.NewInt(37), R: uint256.NewInt(2), S: uint256.NewInt(3),
		}),
		transaction.NewTx(&transaction.AccessListTx{
			ChainID: uint256.NewInt(1), Nonce: 2, GasPrice: uint256.NewInt(10), Gas: 21500, To: &to,
			Value: uint256.NewInt(19), Data: []byte{0x09, 0x0a},
			AccessList: transaction.AccessList{{Address: to, StorageKeys: []types.Hash{storageKey}}},
			V:          uint256.NewInt(0), R: uint256.NewInt(2), S: uint256.NewInt(3),
		}),
		transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID: uint256.NewInt(1), Nonce: 3, GasTipCap: uint256.NewInt(3), GasFeeCap: uint256.NewInt(30), Gas: 22000, To: &to,
			Value: uint256.NewInt(21), Data: []byte{0x03, 0x04},
			AccessList: transaction.AccessList{{Address: to, StorageKeys: []types.Hash{storageKey}}},
			V:          uint256.NewInt(1), R: uint256.NewInt(4), S: uint256.NewInt(5),
		}),
		transaction.NewTx(&transaction.BlobTx{
			ChainID: uint256.NewInt(1), Nonce: 4, GasTipCap: uint256.NewInt(4), GasFeeCap: uint256.NewInt(40), Gas: 23000, To: blobTo,
			Value: uint256.NewInt(22), Data: []byte{0x05, 0x06},
			BlobFeeCap: uint256.NewInt(7), BlobHashes: []types.Hash{blobHash},
			V: uint256.NewInt(1), R: uint256.NewInt(6), S: uint256.NewInt(7),
		}),
		transaction.NewTx(&transaction.SetCodeTx{
			ChainID: uint256.NewInt(1), Nonce: 5, GasTipCap: uint256.NewInt(5), GasFeeCap: uint256.NewInt(50), Gas: 24000, To: &to,
			Value: uint256.NewInt(23), Data: []byte{0x07, 0x08},
			AccessList: transaction.AccessList{{Address: to, StorageKeys: []types.Hash{storageKey}}},
			AuthList: transaction.AuthorizationList{{
				ChainID: *uint256.NewInt(1), Address: delegateTo, Nonce: 9,
				V: uint256.NewInt(1), R: uint256.NewInt(8), S: uint256.NewInt(9),
			}},
			V: uint256.NewInt(1), R: uint256.NewInt(10), S: uint256.NewInt(11),
		}),
	}

	out := make([]*transaction.Transaction, len(raw))
	for i, tx := range raw {
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatalf("EncodeEthereumTransaction[%d]: %v", i, err)
		}
		// Round-trip through the wire encoding, exactly what a real pushed
		// block's TxData holds and what a pool entry looks like once decoded.
		decoded, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			t.Fatalf("DecodeEthereumTransaction[%d]: %v", i, err)
		}
		out[i] = decoded
	}
	return out
}

// blockTxData returns the raw block-body bytes (blockRLP.TxData shape) for
// txs, via EthEncoded -- identical to what Block.EncodeRLP itself uses.
func blockTxData(t *testing.T, txs []*transaction.Transaction) [][]byte {
	t.Helper()
	out := make([][]byte, len(txs))
	for i, tx := range txs {
		enc, err := tx.EthEncoded()
		if err != nil {
			t.Fatalf("EthEncoded[%d]: %v", i, err)
		}
		out[i] = enc
	}
	return out
}

// fakePool is a minimal, mutex-guarded stand-in for the transaction pool's
// own GetTx, used to drive decodeBlockTxsReuse without any txspool
// dependency (common/block cannot import internal/txspool).
type fakePool struct {
	mu sync.RWMutex
	m  map[types.Hash]*transaction.Transaction
}

func newFakePool(entries ...*transaction.Transaction) *fakePool {
	p := &fakePool{m: make(map[types.Hash]*transaction.Transaction)}
	for _, tx := range entries {
		p.m[tx.Hash()] = tx
	}
	return p
}

func (p *fakePool) GetTx(hash types.Hash) *transaction.Transaction {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.m[hash]
}

func (p *fakePool) remove(hash types.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, hash)
}

func (p *fakePool) put(tx *transaction.Transaction) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[tx.Hash()] = tx
}

// TestDecodeBlockTxsReuseEquivalencePerType: with every transaction present
// in the pool, the reuse decode must produce a Transactions list
// element-for-element equal (hash, sender, and re-encoded bytes) to a plain
// decode, for every type. Legacy/AccessList/DynamicFee must additionally be
// REUSED (the exact pool object, by pointer); Blob/SetCode must be decoded
// FRESH even on a hit (PART 1a's own safety boundary), never reused.
func TestDecodeBlockTxsReuseEquivalencePerType(t *testing.T) {
	pooled := sampleTxsOneOfEach(t)
	data := blockTxData(t, pooled)
	pool := newFakePool(pooled...)

	fresh, err := decodeBlockTxs(data)
	if err != nil {
		t.Fatalf("decodeBlockTxs (baseline): %v", err)
	}

	txs, reused, decoded, err := decodeBlockTxsReuse(data, pool.GetTx)
	if err != nil {
		t.Fatalf("decodeBlockTxsReuse: %v", err)
	}
	if len(txs) != len(fresh) {
		t.Fatalf("len(txs) = %d, want %d", len(txs), len(fresh))
	}
	if reused+decoded != len(data) {
		t.Fatalf("reused(%d)+decoded(%d) != %d", reused, decoded, len(data))
	}
	wantReuse := 3 // legacy, access-list, dynamic-fee
	if reused != wantReuse {
		t.Fatalf("reused = %d, want %d (blob/setcode must never be reused)", reused, wantReuse)
	}

	reuseSafe := []bool{true, true, true, false, false} // matches sampleTxsOneOfEach's own order
	for i := range txs {
		freshEnc, err := fresh[i].EthEncoded()
		if err != nil {
			t.Fatalf("fresh[%d].EthEncoded: %v", i, err)
		}
		gotEnc, err := txs[i].EthEncoded()
		if err != nil {
			t.Fatalf("txs[%d].EthEncoded: %v", i, err)
		}
		if string(gotEnc) != string(freshEnc) {
			t.Fatalf("txs[%d] encoding diverges from a fresh decode (type %d)", i, txs[i].Type())
		}
		if txs[i].Hash() != fresh[i].Hash() {
			t.Fatalf("txs[%d] hash diverges from a fresh decode", i)
		}
		if got, want := txs[i].From(), fresh[i].From(); (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Fatalf("txs[%d] sender diverges: got %v want %v", i, got, want)
		}
		if reuseSafe[i] {
			if txs[i] != pooled[i] {
				t.Fatalf("txs[%d] (type %d, reuse-safe) was not the pool's own object", i, txs[i].Type())
			}
		} else if txs[i] == pooled[i] {
			t.Fatalf("txs[%d] (type %d, NOT reuse-safe) was reused despite the exclusion", i, txs[i].Type())
		}
	}
}

// TestDecodeBlockTxsReuseMissPath: an empty pool must decode every
// transaction fresh, byte-for-byte equal to a plain decode.
func TestDecodeBlockTxsReuseMissPath(t *testing.T) {
	pooled := sampleTxsOneOfEach(t)
	data := blockTxData(t, pooled)
	empty := newFakePool()

	fresh, err := decodeBlockTxs(data)
	if err != nil {
		t.Fatalf("decodeBlockTxs (baseline): %v", err)
	}
	txs, reused, decoded, err := decodeBlockTxsReuse(data, empty.GetTx)
	if err != nil {
		t.Fatalf("decodeBlockTxsReuse: %v", err)
	}
	if reused != 0 || decoded != len(data) {
		t.Fatalf("reused=%d decoded=%d, want 0/%d on a total miss", reused, decoded, len(data))
	}
	for i := range txs {
		if txs[i].Hash() != fresh[i].Hash() {
			t.Fatalf("txs[%d] hash diverges on the miss path", i)
		}
	}
}

// TestDecodeBlockTxsReuseNilLookupMatchesPlainDecode: a nil lookup (switch
// off) must behave exactly like decodeBlockTxs.
func TestDecodeBlockTxsReuseNilLookupMatchesPlainDecode(t *testing.T) {
	pooled := sampleTxsOneOfEach(t)
	data := blockTxData(t, pooled)

	txs, reused, decoded, err := decodeBlockTxsReuse(data, nil)
	if err != nil {
		t.Fatalf("decodeBlockTxsReuse(nil lookup): %v", err)
	}
	if reused != 0 || decoded != len(data) {
		t.Fatalf("reused=%d decoded=%d, want 0/%d for a nil lookup (falls back to decodeBlockTxs directly)", reused, decoded, len(data))
	}
	if len(txs) != len(data) {
		t.Fatalf("len(txs) = %d, want %d", len(txs), len(data))
	}
}

// TestDecodeBlockTxsReuseOnlyExactHashMatches: with only ONE of two
// distinct transactions in the pool, the reuse decode must reuse that one
// and decode the OTHER fresh -- never cross-match by anything short of the
// full 32-byte hash.
func TestDecodeBlockTxsReuseOnlyExactHashMatches(t *testing.T) {
	pooled := sampleTxsOneOfEach(t)
	data := blockTxData(t, pooled)
	// Only the legacy transaction (index 0) is "in the pool".
	pool := newFakePool(pooled[0])

	txs, reused, decoded, err := decodeBlockTxsReuse(data, pool.GetTx)
	if err != nil {
		t.Fatalf("decodeBlockTxsReuse: %v", err)
	}
	if reused != 1 || decoded != len(data)-1 {
		t.Fatalf("reused=%d decoded=%d, want 1/%d", reused, decoded, len(data)-1)
	}
	if txs[0] != pooled[0] {
		t.Fatal("the one pooled transaction was not reused")
	}
	for i := 1; i < len(txs); i++ {
		if txs[i] == pooled[i] {
			t.Fatalf("txs[%d] was reused although it was never in the pool", i)
		}
		if txs[i].Hash() != pooled[i].Hash() {
			t.Fatalf("txs[%d] hash mismatch after a fresh decode", i)
		}
	}
}

// TestDecodeBlockTxsReuseConcurrentEviction: a pool entry removed
// concurrently with the block's own decode must not corrupt or crash the
// decode -- the decode either catches the object before removal (reuse) or
// after (a clean miss, decoded fresh); either is correct. Run under -race.
func TestDecodeBlockTxsReuseConcurrentEviction(t *testing.T) {
	pooled := sampleTxsOneOfEach(t)
	data := blockTxData(t, pooled)
	pool := newFakePool(pooled...)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, tx := range pooled {
			pool.remove(tx.Hash())
		}
	}()

	txs, _, _, err := decodeBlockTxsReuse(data, pool.GetTx)
	wg.Wait()
	if err != nil {
		t.Fatalf("decodeBlockTxsReuse under concurrent eviction: %v", err)
	}
	for i := range txs {
		if txs[i] == nil {
			t.Fatalf("txs[%d] is nil after concurrent eviction", i)
		}
		if txs[i].Hash() != pooled[i].Hash() {
			t.Fatalf("txs[%d] hash mismatch after concurrent eviction", i)
		}
	}
}

// TestDecodeBlockTxsReuseConcurrentInsert: decoding while the pool is
// concurrently gaining new entries (a live insert stream) must not race or
// misbehave. Run under -race.
func TestDecodeBlockTxsReuseConcurrentInsert(t *testing.T) {
	pooled := sampleTxsOneOfEach(t)
	data := blockTxData(t, pooled)
	pool := newFakePool() // starts empty; filled concurrently below

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, tx := range pooled {
			pool.put(tx)
		}
	}()

	txs, _, _, err := decodeBlockTxsReuse(data, pool.GetTx)
	wg.Wait()
	if err != nil {
		t.Fatalf("decodeBlockTxsReuse under concurrent insert: %v", err)
	}
	for i := range txs {
		if txs[i].Hash() != pooled[i].Hash() {
			t.Fatalf("txs[%d] hash mismatch under concurrent insert", i)
		}
	}
}
