// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// The batched sender path (crypto.EcrecoverIFMAEnabled): replay-protected
// legacy transactions on the uint64 fast path are taken up to
// sender8Group at a time, their signing hashes and address hashes
// four-way Keccak as in the four-way path, and their public keys
// recovered by one crypto.RecoverPubkeysBatch call (the AVX-512 IFMA
// kernel, eight lanes at a time; a larger call shares its inversions and
// interleaves its square roots). Validation, results and errors are
// recoverPubInto's, lane by lane.

// sender8Group is the most transactions one kernel call is given.
const sender8Group = 64

// sender8Scratch holds one group's buffers.
type sender8Scratch struct {
	pay   [sender8Group][crypto.Keccak256OneBlockMax]byte
	hash  [sender8Group][32]byte
	sig   [sender8Group][65]byte
	pub   [sender8Group][65]byte
	err   [sender8Group]error
	lane  [sender8Group]int // group position of each recovery
	idx   [sender8Group]int
	inner [sender8Group]*LegacyTx
	vb    [sender8Group]uint64
	sigh  [sender8Group]types.Hash
	rerr  [sender8Group]error
	pubs  [sender8Group][]byte
	out   [sender8Group]types.Address
}

// sendersBatch8 is SendersBatch with the recoveries batched.
func (s EIP155Signer) sendersBatch8(signer Signer, txs []*Transaction, addrs []types.Address, errs []error) {
	var cid uint256.Int
	if s.chainId != nil {
		cid.SetFromBig(s.chainId)
	}
	bs := senderBatchPool.Get().(*senderBatchScratch)
	defer senderBatchPool.Put(bs)
	x := &bs.x8
	n := 0
	for i, tx := range txs {
		if tx == nil {
			continue
		}
		in, v, err, fast := s.legacyFastPrecheck(tx)
		if !fast {
			addrs[i], errs[i] = signer.Sender(tx)
			continue
		}
		if err != nil {
			addrs[i], errs[i] = types.Address{}, err
			continue
		}
		x.idx[n], x.inner[n], x.vb[n] = i, in, v
		n++
		if n == sender8Group {
			s.recover8(x, &cid, txs, n, addrs, errs)
			n = 0
		}
	}
	if n > 0 {
		s.recover8(x, &cid, txs, n, addrs, errs)
	}
	clear(x.err[:])
	clear(x.rerr[:])
	clear(x.inner[:])
	clear(x.pubs[:])
}

// recover8 recovers the senders of the n <= sender8Group prechecked
// legacy transactions txs[x.idx[k]] (x.inner[k], recovery value x.vb[k]).
func (s EIP155Signer) recover8(x *sender8Scratch, cid *uint256.Int, txs []*Transaction, n int, addrs []types.Address,
	errs []error) {
	idx, inner, vb, sigh, rerr, pubs := &x.idx, &x.inner, &x.vb, &x.sigh, &x.rerr, &x.pubs
	// Signing hashes: four-way for each full quarter of one-block payloads.
	for q := 0; q < n; q += 4 {
		done := false
		if q+4 <= n && keccakBatchOn() {
			var in [4][]byte
			fits := true
			for j := 0; j < 4; j++ {
				tx := txs[idx[q+j]]
				size := legacySigningContentSize(tx, cid)
				if size+2 >= crypto.Keccak256OneBlockMax {
					fits = false
					break
				}
				in[j] = appendLegacySigningPayload(x.pay[q+j][:0], tx, cid, size)
			}
			done = fits && crypto.Keccak256x4OneBlock(&in, (*[4]types.Hash)(sigh[q:q+4]))
		}
		if !done {
			for j := q; j < n && j < q+4; j++ {
				sigh[j], _ = s.Hash(txs[idx[j]])
			}
		}
	}
	// Validation as recoverPubInto, then one batch of recoveries.
	m := 0
	for k := 0; k < n; k++ {
		rerr[k] = nil
		R, S, v := inner[k].R, inner[k].S, vb[k]
		if R == nil || S == nil || v >= 256 {
			rerr[k] = ErrInvalidSig
			continue
		}
		V := byte(v - 27)
		if !crypto.ValidateSignatureValues(V, R, S, true) {
			rerr[k] = ErrInvalidSig
			continue
		}
		x.hash[m] = sigh[k]
		R.WriteToSlice(x.sig[m][:32])
		S.WriteToSlice(x.sig[m][32:64])
		x.sig[m][64] = V
		x.lane[m] = k
		m++
		if countRecoveries.Load() {
			recoveries.Add(1)
		}
	}
	crypto.RecoverPubkeysBatch(x.hash[:m], x.sig[:m], x.pub[:m], x.err[:m])
	for k := 0; k < n; k++ {
		pubs[k] = x.pub[0][1:] // a dummy lane; its digest is discarded
	}
	for j := 0; j < m; j++ {
		k := x.lane[j]
		switch {
		case x.err[j] != nil:
			rerr[k] = x.err[j]
		case x.pub[j][0] != 4:
			rerr[k] = errors.New("invalid public key")
		default:
			pubs[k] = x.pub[j][1:]
		}
	}
	// Addresses: four-way for each full quarter.
	out := &x.out
	for q := 0; q < n; q += 4 {
		var digests [4]types.Hash
		if q+4 <= n && keccakBatchOn() && crypto.Keccak256x4OneBlock((*[4][]byte)(pubs[q:q+4]), &digests) {
			for j := 0; j < 4; j++ {
				copy(out[q+j][:], digests[j][12:])
			}
			continue
		}
		for j := q; j < n && j < q+4; j++ {
			if rerr[j] == nil {
				out[j] = pubkeyAddress(pubs[j])
			}
		}
	}
	for k := 0; k < n; k++ {
		i := idx[k]
		if rerr[k] != nil {
			addrs[i], errs[i] = types.Address{}, rerr[k]
			continue
		}
		addrs[i], errs[i] = out[k], nil
	}
}

// sendersMemoChunk bounds one SendersMemo pass, keeping its buffers on the
// stack.
const sendersMemoChunk = 64

// SendersMemo is Sender(signer, tx) for every non-nil tx of txs, with the
// recoveries batched: transactions the memo or the sender cache answers
// for are settled first, the rest go through SendersBatch together, and
// each recovered sender is memoised and cached as Sender would. When addrs
// is non-nil (at least len(txs) long), addrs[i] is the sender of txs[i], or
// the zero address when it does not recover. Without
// crypto.EcrecoverIFMAEnabled this is Sender one transaction at a time.
func SendersMemo(signer Signer, txs []*Transaction, addrs []types.Address) {
	if !crypto.EcrecoverIFMAEnabled {
		for i, tx := range txs {
			if tx == nil {
				continue
			}
			a, err := Sender(signer, tx)
			if addrs != nil {
				if err != nil {
					a = types.Address{}
				}
				addrs[i] = a
			}
		}
		return
	}
	var (
		miss  [sendersMemoChunk]*Transaction
		pos   [sendersMemoChunk]int
		found [sendersMemoChunk]types.Address
		errs  [sendersMemoChunk]error
	)
	for lo := 0; lo < len(txs); lo += sendersMemoChunk {
		hi := min(lo+sendersMemoChunk, len(txs))
		m := 0
		for i := lo; i < hi; i++ {
			tx := txs[i]
			if tx == nil {
				continue
			}
			if sc := tx.from.Load(); sc != nil && sc.signer.Equal(signer) {
				if addrs != nil {
					addrs[i] = sc.from
				}
				continue
			}
			if a, ok := senderCacheGet(tx.Hash(), signer); ok {
				tx.storeSenderMemo(signer, a)
				if addrs != nil {
					addrs[i] = a
				}
				continue
			}
			miss[m], pos[m] = tx, i
			m++
		}
		if m == 0 {
			continue
		}
		SendersBatch(signer, miss[:m], found[:m], errs[:m])
		for k := 0; k < m; k++ {
			tx, a := miss[k], found[k]
			if errs[k] != nil {
				a = types.Address{}
			} else {
				tx.storeSenderMemo(signer, a)
				senderCachePut(tx.Hash(), signer, a)
			}
			if addrs != nil {
				addrs[pos[k]] = a
			}
		}
		clear(miss[:m])
		clear(errs[:m])
	}
}
