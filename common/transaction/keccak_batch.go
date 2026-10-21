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

// With AVX-512 (crypto.KeccakX8Enabled, N42_KECCAK_X8) the batches are
// eight wide and one eight-way permutation costs about what one four-way
// AVX2 permutation did.

// keccakBatchMax is the widest batch the loops below collect.
const keccakBatchMax = 8

// keccakBatchOn reports whether the multi-way batching is worth taking.
func keccakBatchOn() bool {
	return crypto.KeccakOneBlockEnabled && crypto.KeccakX4Enabled
}

// keccakBatchWidth is the batch the loops collect before hashing: eight
// with the AVX-512 eight-way permutation, else four.
func keccakBatchWidth() int {
	if crypto.KeccakX8Enabled {
		return 8
	}
	return 4
}

// keccakOneBlockN sets out[i] = Keccak256Hash(in[i]) for the first n
// inputs (n <= 8), each shorter than the Keccak rate: eight-way when n is 8
// and AVX-512 is on, four-way per remaining group of four, one by one for
// the rest.
func keccakOneBlockN(in *[keccakBatchMax][]byte, out *[keccakBatchMax]types.Hash, n int) {
	off := 0
	if n == 8 && crypto.KeccakX8Enabled && crypto.Keccak256x8OneBlock(in, out) {
		return
	}
	for ; n-off >= 4; off += 4 {
		if !crypto.Keccak256x4OneBlock((*[4][]byte)(in[off:off+4]), (*[4]types.Hash)(out[off:off+4])) {
			break
		}
	}
	for ; off < n; off++ {
		out[off] = crypto.Keccak256HashOne(in[off])
	}
}

// HashBatch memoises the hash of every transaction of txs (nil entries are
// skipped), as tx.Hash() on each would, hashing single-block cached
// encodings four (eight with AVX-512) at a time. A transaction whose hash is ready or claimed by
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
		claimed [keccakBatchMax]*Transaction
		in      [keccakBatchMax][]byte
		out     [keccakBatchMax]types.Hash
		n       int
		width   = keccakBatchWidth()
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
		if n == width {
			publishHashes(&claimed, &in, &out, n)
			n = 0
		}
	}
	publishHashes(&claimed, &in, &out, n)
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

// publishHashes hashes the first n claimed encodings (multi-way where
// possible) and publishes them as Hash() does after its claim.
func publishHashes(claimed *[keccakBatchMax]*Transaction, in *[keccakBatchMax][]byte, out *[keccakBatchMax]types.Hash, n int) {
	keccakOneBlockN(in, out, n)
	for i := 0; i < n; i++ {
		claimed[i].hashVal = out[i]
		claimed[i].hashState.Store(hashReady)
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

// senderBatchScratch holds a batch's recovery cgo buffers (see
// recoverScratch) and signing payload buffers.
type senderBatchScratch struct {
	sc  [keccakBatchMax]recoverScratch
	pay [keccakBatchMax][crypto.Keccak256OneBlockMax]byte
}

var senderBatchPool = sync.Pool{New: func() any { return new(senderBatchScratch) }}

// SendersBatch sets addrs[i], errs[i] = signer.Sender(txs[i]) for every
// non-nil txs[i] (nil entries are left untouched). Replay-protected legacy
// transactions on the uint64 fast path are processed four (eight with
// AVX-512) at a time: their signing hashes, then their recoveries one by
// one, then their address hashes, each Keccak step one multi-way
// permutation. Every other transaction goes through signer.Sender. Results
// are identical to the one-at-a-time calls. addrs and errs must be at least as long as txs.
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
		idx   [keccakBatchMax]int
		inner [keccakBatchMax]*LegacyTx
		vb    [keccakBatchMax]uint64
		n     int
		width = keccakBatchWidth()
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
		if n == width {
			es.recoverBatch(bs, &cid, txs, &idx, &inner, &vb, n, addrs, errs)
			n = 0
		}
	}
	if n > 0 {
		es.recoverBatch(bs, &cid, txs, &idx, &inner, &vb, n, addrs, errs)
	}
}

// recoverBatch recovers the senders of the n prechecked legacy
// transactions txs[idx[0..n-1]].
func (s EIP155Signer) recoverBatch(bs *senderBatchScratch, cid *uint256.Int, txs []*Transaction, idx *[keccakBatchMax]int,
	inner *[keccakBatchMax]*LegacyTx, vb *[keccakBatchMax]uint64, n int, addrs []types.Address, errs []error) {
	var (
		in  [keccakBatchMax][]byte
		sig [keccakBatchMax]types.Hash
	)
	// Signing hashes: multi-way when every payload fits one block.
	fits := true
	for j := 0; j < n; j++ {
		tx := txs[idx[j]]
		size := legacySigningContentSize(tx, cid)
		if size+2 >= crypto.Keccak256OneBlockMax {
			fits = false
			break
		}
		in[j] = appendLegacySigningPayload(bs.pay[j][:0], tx, cid, size)
	}
	if fits {
		keccakOneBlockN(&in, &sig, n)
	} else {
		for j := 0; j < n; j++ {
			sig[j], _ = s.Hash(txs[idx[j]])
		}
	}
	// Recoveries, one by one; then the addresses multi-way.
	var (
		rerr [keccakBatchMax]error
		pubs [keccakBatchMax][]byte
	)
	for j := 0; j < n; j++ {
		pubs[j], rerr[j] = recoverPubInto(&bs.sc[j], sig[j], inner[j].R, inner[j].S, vb[j], true)
		if rerr[j] != nil {
			pubs[j] = bs.sc[j].pub[1:] // a dummy lane; its digest is discarded
		}
	}
	var digests [keccakBatchMax]types.Hash
	keccakOneBlockN(&pubs, &digests, n)
	for j := 0; j < n; j++ {
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
