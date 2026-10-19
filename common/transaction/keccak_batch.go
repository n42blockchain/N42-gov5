// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// Four-way Keccak batching for the two per-transaction hash loops of a frame:
// the hash memoisation after a decode and the sender recovery. A simple
// transfer's encoding (~105-110 bytes), its EIP-155 signing payload (~45-70
// bytes) and its recovered public key (64 bytes) each fit one Keccak rate
// block, so four of them share one AVX2 four-way permutation
// (crypto.Keccak256x4OneBlock): ~110 ns per hash against ~265 ns for a lone
// single-block hash. Results are bit-identical to the one-at-a-time paths;
// N42_KECCAK_ONEBLOCK=0 turns the batching off with the rest of the
// single-block path.

// keccakBatchOn reports whether the four-way batching is worth taking.
func keccakBatchOn() bool {
	return crypto.KeccakOneBlockEnabled && crypto.KeccakX4Enabled
}

// HashBatch memoises the hash of every transaction of txs (nil entries are
// skipped), as tx.Hash() on each would, hashing single-block cached
// encodings four at a time. A transaction whose hash is ready or claimed by
// another goroutine, or that has no single-block cached encoding, goes
// through tx.Hash().
func HashBatch(txs []*Transaction) {
	if !keccakBatchOn() {
		for _, tx := range txs {
			if tx != nil {
				tx.Hash()
			}
		}
		return
	}
	var (
		claimed [4]*Transaction
		in      [4][]byte
		out     [4]types.Hash
		n       int
	)
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		enc := tx.singleBlockEncoding()
		if enc == nil || !tx.hashState.CompareAndSwap(hashNone, hashWriting) {
			tx.Hash()
			continue
		}
		claimed[n], in[n] = tx, enc
		n++
		if n == 4 {
			publishHashes4(&claimed, &in, &out, 4)
			n = 0
		}
	}
	publishHashes4(&claimed, &in, &out, n)
}

// singleBlockEncoding returns tx's cached consensus encoding when its hash
// is keccak of exactly that encoding and it fits one Keccak rate block.
func (tx *Transaction) singleBlockEncoding() []byte {
	if !hashFromEncoding(tx.Type()) {
		return nil
	}
	p := tx.enc.Load()
	if p == nil || len(*p) == 0 || len(*p) >= crypto.Keccak256OneBlockMax {
		return nil
	}
	return *p
}

// publishHashes4 hashes the first n claimed encodings (four-way when n is
// 4) and publishes them as Hash() does after its claim.
func publishHashes4(claimed *[4]*Transaction, in *[4][]byte, out *[4]types.Hash, n int) {
	if n == 4 && crypto.Keccak256x4OneBlock(in, out) {
		for i := 0; i < 4; i++ {
			claimed[i].hashVal = out[i]
			claimed[i].hashState.Store(hashReady)
			claimed[i], in[i] = nil, nil
		}
		return
	}
	for i := 0; i < n; i++ {
		tx := claimed[i]
		tx.hashVal = tx.computeHash()
		tx.hashState.Store(hashReady)
		claimed[i], in[i] = nil, nil
	}
}

// legacyFastSigner returns the EIP155Signer whose legacySenderFast is the
// first step of signer.Sender for a legacy transaction.
func legacyFastSigner(signer Signer) (EIP155Signer, bool) {
	switch s := signer.(type) {
	case londonSigner:
		return s.EIP155Signer, true
	case eip2930Signer:
		return s.EIP155Signer, true
	case EIP155Signer:
		return s, true
	}
	return EIP155Signer{}, false
}

// senderBatchScratch holds four recoveries' cgo buffers (see recoverScratch)
// and four signing payload buffers.
type senderBatchScratch struct {
	sc  [4]recoverScratch
	pay [4][crypto.Keccak256OneBlockMax]byte
}

var senderBatchPool = sync.Pool{New: func() any { return new(senderBatchScratch) }}

// SendersBatch sets addrs[i], errs[i] = signer.Sender(txs[i]) for every
// non-nil txs[i] (nil entries are left untouched). Replay-protected legacy
// transactions on the uint64 fast path are processed four at a time: their
// signing hashes, then their recoveries one by one, then their address
// hashes, each Keccak step a four-way permutation. Every other transaction
// goes through signer.Sender. Results are identical to the one-at-a-time
// calls. addrs and errs must be at least as long as txs.
func SendersBatch(signer Signer, txs []*Transaction, addrs []types.Address, errs []error) {
	es, ok := legacyFastSigner(signer)
	if !ok || !keccakBatchOn() {
		for i, tx := range txs {
			if tx != nil {
				addrs[i], errs[i] = signer.Sender(tx)
			}
		}
		return
	}
	var cid uint256.Int
	if es.chainId != nil {
		cid.SetFromBig(es.chainId)
	}
	bs := senderBatchPool.Get().(*senderBatchScratch)
	defer senderBatchPool.Put(bs)
	var (
		idx   [4]int
		inner [4]*LegacyTx
		vb    [4]uint64
		n     int
	)
	for i, tx := range txs {
		if tx == nil {
			continue
		}
		in, v, err, fast := es.legacyFastPrecheck(tx)
		if !fast {
			addrs[i], errs[i] = signer.Sender(tx)
			continue
		}
		if err != nil {
			addrs[i], errs[i] = types.Address{}, err
			continue
		}
		idx[n], inner[n], vb[n] = i, in, v
		n++
		if n == 4 {
			es.recover4(bs, &cid, txs, &idx, &inner, &vb, addrs, errs)
			n = 0
		}
	}
	for j := 0; j < n; j++ {
		i := idx[j]
		h, _ := es.Hash(txs[i])
		addrs[i], errs[i] = recoverPlainV(h, inner[j].R, inner[j].S, vb[j], true)
	}
}

// recover4 recovers the senders of the four prechecked legacy transactions
// txs[idx[0..3]].
func (s EIP155Signer) recover4(bs *senderBatchScratch, cid *uint256.Int, txs []*Transaction, idx *[4]int, inner *[4]*LegacyTx,
	vb *[4]uint64, addrs []types.Address, errs []error) {
	var (
		in  [4][]byte
		sig [4]types.Hash
	)
	// Signing hashes: four-way when every payload fits one block.
	fits := true
	for j := 0; j < 4; j++ {
		tx := txs[idx[j]]
		size := legacySigningContentSize(tx, cid)
		if size+2 >= crypto.Keccak256OneBlockMax {
			fits = false
			break
		}
		in[j] = appendLegacySigningPayload(bs.pay[j][:0], tx, cid, size)
	}
	if !fits || !crypto.Keccak256x4OneBlock(&in, &sig) {
		for j := 0; j < 4; j++ {
			sig[j], _ = s.Hash(txs[idx[j]])
		}
	}
	// Recoveries, one by one; then the addresses four-way.
	var (
		rerr [4]error
		pubs [4][]byte
	)
	for j := 0; j < 4; j++ {
		pubs[j], rerr[j] = recoverPubInto(&bs.sc[j], sig[j], inner[j].R, inner[j].S, vb[j], true)
		if rerr[j] != nil {
			pubs[j] = bs.sc[j].pub[1:] // a dummy lane; its digest is discarded
		}
	}
	var digests [4]types.Hash
	if !crypto.Keccak256x4OneBlock(&pubs, &digests) {
		for j := 0; j < 4; j++ {
			digests[j] = crypto.Keccak256Hash(pubs[j])
		}
	}
	for j := 0; j < 4; j++ {
		i := idx[j]
		if rerr[j] != nil {
			addrs[i], errs[i] = types.Address{}, rerr[j]
			continue
		}
		var a types.Address
		copy(a[:], digests[j][12:])
		addrs[i], errs[i] = a, nil
	}
}
