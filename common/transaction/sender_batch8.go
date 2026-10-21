// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// The eight-way sender path (crypto.EcrecoverIFMAEnabled): replay-protected
// legacy transactions on the uint64 fast path are taken eight at a time,
// their signing hashes and address hashes four-way Keccak as in the
// four-way path, and their public keys recovered by one
// crypto.RecoverPubkeysBatch call (the AVX-512 IFMA kernel). Validation,
// results and errors are recoverPubInto's, lane by lane.

// sender8Scratch holds one group's buffers.
type sender8Scratch struct {
	pay  [8][crypto.Keccak256OneBlockMax]byte
	hash [8][32]byte
	sig  [8][65]byte
	pub  [8][65]byte
	err  [8]error
	lane [8]int // group position of each recovery
}

// sendersBatch8 is SendersBatch with the recoveries eight at a time.
func (s EIP155Signer) sendersBatch8(signer Signer, txs []*Transaction, addrs []types.Address, errs []error) {
	var cid uint256.Int
	if s.chainId != nil {
		cid.SetFromBig(s.chainId)
	}
	bs := senderBatchPool.Get().(*senderBatchScratch)
	defer senderBatchPool.Put(bs)
	var (
		idx   [8]int
		inner [8]*LegacyTx
		vb    [8]uint64
		n     int
	)
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
		idx[n], inner[n], vb[n] = i, in, v
		n++
		if n == 8 {
			s.recover8(&bs.x8, &cid, txs, idx[:], &inner, &vb, addrs, errs)
			n = 0
		}
	}
	if n > 0 {
		s.recover8(&bs.x8, &cid, txs, idx[:n], &inner, &vb, addrs, errs)
	}
	clear(bs.x8.err[:])
}

// recover8 recovers the senders of the len(idx) <= 8 prechecked legacy
// transactions txs[idx[k]].
func (s EIP155Signer) recover8(x *sender8Scratch, cid *uint256.Int, txs []*Transaction, idx []int, inner *[8]*LegacyTx,
	vb *[8]uint64, addrs []types.Address, errs []error) {
	n := len(idx)
	var sigh [8]types.Hash
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
	var rerr [8]error
	m := 0
	for k := 0; k < n; k++ {
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
	var pubs [8][]byte
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
	var out [8]types.Address
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
