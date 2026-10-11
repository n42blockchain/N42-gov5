// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/attest"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// TestFrameAttestationReplacesRecovery (P8b): a follower that is one of
// seven validators (f = 2, committees of 3) admits frames and imports
// blocks of them while the other validators attest their committees'
// frames. It recovers exactly the frames whose committee it is on and
// takes every other frame's senders from three matching attestations,
// with zero recoveries. With one member's attestation missing on some
// frames it falls back to recovering exactly those. The executed results
// equal a run without attestation.
func TestFrameAttestationReplacesRecovery(t *testing.T) {
	var roots [][]types.Hash
	for i, missing := range []bool{false, true} {
		roots = append(roots, runFrameAttestation(t, byte(120+i), true, missing))
	}
	// The plain run last: its sender jobs fill the process-wide cache.
	plain := runFrameAttestation(t, 120, false, false)
	require.Equal(t, plain, roots[0], "attested senders changed an executed result")
}

func runFrameAttestation(t *testing.T, salt byte, attestOn, missing bool) []types.Hash {
	const (
		blocks   = 4
		frames   = 14
		perFrame = 100
		n, f     = 7, 2
	)
	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	prevGas := lagChainGasLimit
	lagChainGasLimit = 1_000_000_000
	defer func() { lagChainGasLimit = prevGas }()

	keys := frametest.Keys(t, 41, salt)
	lc := newLagChainKeys(t, keys[1:])
	lc.bc.SetParallelEVM(true)
	st := store.NewWithKeep(64<<20, 16)
	lc.bc.SetFrameStore(st)
	signer := transaction.LatestSignerForChainID(lc.cfg.ChainID)
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 4)
	fb := frametest.NewBuilder(keys[0], lc.cfg.ChainID, keys[1:])

	// Seven validators with BLS keys; this node is the first.
	vkeys := map[types.Address]blscommon.SecretKey{}
	var vaddrs []types.Address
	for i := 0; i < n; i++ {
		var a types.Address
		_, _ = rand.Read(a[:])
		k, err := bls.RandKey()
		require.NoError(t, err)
		vkeys[a], vaddrs = k, append(vaddrs, a)
	}
	self := vaddrs[0]
	v := &attest.Validators{Sorted: attest.SortAddresses(vaddrs), F: f, Verify: func(a types.Address, msg, sig []byte) bool {
		s, err := bls.SignatureFromBytes(sig)
		return err == nil && s.Verify(vkeys[a].PublicKey(), msg)
	}}
	var pool *attest.Pool
	if attestOn {
		pool = attest.NewPool(attest.Config{Validators: func() *attest.Validators { return v },
			Self: func() types.Address { return self }, Signer: func() transaction.Signer { return signer },
			Sign: func(msg []byte) ([]byte, error) { return vkeys[self].Sign(msg).Marshal(), nil },
			Send: func([]byte) {}, Store: st, CommitteeSize: 3})
		rec.SetSource(pool)
		lc.bc.SetFrameSenderSource(pool)
	}
	rec.Start(context.Background())
	defer rec.Stop()

	// Everything signed and every honest senders list computed before
	// counting: the other validators' recoveries are not this node's.
	type frameData struct {
		raw     []byte
		h       types.Hash
		senders []types.Address
	}
	all := make([][]frameData, blocks)
	for b := range all {
		raws, hashes, _ := fb.Frames(t, frames, perFrame)
		for i := range raws {
			ss, failed, err := frame.RecoverSenders(signer, raws[i])
			require.NoError(t, err)
			require.Zero(t, failed)
			all[b] = append(all[b], frameData{raws[i], hashes[i], ss.Addrs})
		}
	}
	// attestFrames delivers the other committee members' attestations,
	// one batch per member, over the wire encoding. With missing, the last
	// member withholds every third frame it is on.
	var wantRecovered, withheld uint64
	attestFrames := func(fs []frameData) {
		perMember := map[types.Address][]attest.Entry{}
		for i, fd := range fs {
			var members []types.Address
			for _, k := range attest.Committee(fd.h, n, 3) {
				members = append(members, v.Sorted[k])
			}
			mine := false
			for _, m := range members {
				mine = mine || m == self
			}
			if mine {
				wantRecovered += uint64(len(fd.senders))
				continue
			}
			for k, m := range members {
				if missing && k == 2 && i%3 == 0 {
					wantRecovered += uint64(len(fd.senders))
					withheld++
					continue
				}
				perMember[m] = append(perMember[m], attest.Entry{Frame: fd.h, Digest: attest.Digest(fd.senders), Senders: fd.senders})
			}
		}
		for m, es := range perMember {
			msg := attest.SigningHash(m, es)
			b := &attest.Batch{Attester: m, Entries: es, Sig: vkeys[m].Sign(msg[:]).Marshal()}
			pool.HandleWire(attest.EncodeBody(b))
		}
	}
	waitLag := func(num uint64) {
		require.Eventually(t, func() bool { _, last, _ := lc.bc.LagStats(); return last >= num },
			60*time.Second, 2*time.Millisecond, "lag executor never reached %d", num)
	}

	stop := transaction.CountSenderRecoveriesForTest()
	defer stop()
	var roots []types.Hash
	parent := lc.bc.genesisBlock.Header().(*block.Header)
	for num := uint64(1); num <= blocks; num++ {
		fs := all[num-1]
		var raws [][]byte
		var hashes []types.Hash
		for _, fd := range fs {
			raws, hashes = append(raws, fd.raw), append(hashes, fd.h)
		}
		// Odd blocks: attestations before the frames; even: after.
		if attestOn && num%2 == 1 {
			attestFrames(fs)
		}
		for i := range raws {
			require.True(t, st.Put(hashes[i], raws[i]))
		}
		for rec.Pending() > 0 {
			time.Sleep(time.Millisecond)
		}
		if attestOn && num%2 == 0 {
			attestFrames(fs)
		}
		if num >= 2 {
			waitLag(num - 2)
		}
		hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
			Time: parent.Time + 10, BaseFee: parent.BaseFee, Difficulty: parentDifficulty(parent),
			Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}
		hdr.Number.AddUint64(hdr.Number, 1)
		if bf := misc.CalcBaseFee(lc.cfg, parent); bf != nil {
			hdr.BaseFee.SetFromBig(bf)
		}
		rtx, err := lc.db.BeginRo(lc.bc.ctx)
		require.NoError(t, err)
		res, _, err := lc.bc.LaggedExecutedResult(rtx, hdr, nil)
		rtx.Rollback()
		require.NoError(t, err)
		hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
		hdr.SetFrameTxRoot(block.FrameTxRoot(hashes))
		w := overWire(t, block.NewLaggedBlock(hdr, hashes, raws))
		checked, retry, err := lc.bc.CheckDeferredBlock(w)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "availability check of block %d", num)
		parent = w.Header().(*block.Header)
	}
	waitLag(blocks)
	got := stop()
	require.Equal(t, uint64(frames*perFrame*21000), lc.result(parent.Hash()).GasUsed, "the last block did not execute every transfer")
	for h := parent; h.Number.Uint64() > 0; {
		roots = append(roots, lc.result(h.Hash()).Root)
		ph, err := lc.bc.GetHeaderByHash(h.ParentHash)
		require.NoError(t, err)
		require.NotNil(t, ph)
		h = ph.(*block.Header)
	}
	if !attestOn {
		return roots
	}
	total := uint64(blocks * frames * perFrame)
	t.Logf("missing=%v: recoveries %d of %d transactions (want %d, %d frames withheld); admission %+v; pool %s",
		missing, got, total, wantRecovered, withheld, rec.Stats(), pool.Stats())
	require.Equal(t, wantRecovered, got, "this node must recover exactly its committee frames and the unattested ones")
	require.Less(t, got*4, total*3, "attestation saved nothing (expected 3/7 of the frames recovered)")
	if missing {
		require.Equal(t, withheld, pool.Stats().Fallback, "fallback recoveries")
	}
	return roots
}
