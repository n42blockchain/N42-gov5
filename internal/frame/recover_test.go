// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

var recoverChainID = big.NewInt(4242)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestSenderRecovererCachesAtAdmission: every frame the store admits, on
// any path (plain admit, bypass admit as for pulls and local frames), gets
// its senders recovered in the background and cached with it.
func TestSenderRecovererCachesAtAdmission(t *testing.T) {
	keys := frametest.Keys(t, 5, 9)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, txs := fb.Frames(t, 6, 20)
	signer := transaction.LatestSignerForChainID(recoverChainID)

	st := store.New(1 << 24)
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 3)
	rec.Start(context.Background())
	defer rec.Stop()

	for i := range raws {
		bypass := i%2 == 1
		if st.Admit(hashes[i], store.Entry{Raw: raws[i]}, bypass) != store.Stored {
			t.Fatalf("frame %d not stored", i)
		}
	}
	waitFor(t, "recovery", func() bool { return rec.Recovered() == uint64(len(raws)) })
	if p := rec.Pending(); p != 0 {
		t.Fatalf("pending %d after all recovered", p)
	}
	stats := st.Stats()
	if stats.WithSenders != len(raws) || stats.Recovered != uint64(len(raws)) || stats.RecoverPending != 0 {
		t.Fatalf("store stats %+v", stats)
	}
	for i, h := range hashes {
		ss := st.Senders(h)
		if ss == nil || len(ss.Addrs) != 20 {
			t.Fatalf("frame %d: senders %+v", i, ss)
		}
		for j, a := range ss.Addrs {
			want := crypto.PubkeyToAddress(keys[1+((i*20+j)%4)].PublicKey)
			if a != want {
				t.Fatalf("frame %d tx %d: cached %s want %s", i, j, a, want)
			}
		}
	}
	// A frame dropped from the store releases its cache count.
	st.Remove(hashes[:1])
	if got := st.Stats().WithSenders; got != len(raws)-1 {
		t.Fatalf("withSenders after remove = %d", got)
	}
	_ = txs
}

// TestApplySendersUsesTheMemoOnlyUnderItsSigner: an attached sender is what
// Sender returns under the signer it was recovered with (no recovery runs),
// and a block whose signer differs re-derives from the signature, so the
// cache can never change a verdict.
func TestApplySendersUsesTheMemoOnlyUnderItsSigner(t *testing.T) {
	keys := frametest.Keys(t, 3, 11)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, _, _ := fb.Frames(t, 1, 4)
	signer := transaction.LatestSignerForChainID(recoverChainID)

	ss, failed, err := frame.RecoverSenders(signer, raws[0])
	if err != nil || failed != 0 || len(ss.Addrs) != 4 {
		t.Fatalf("recover: %v failed=%d", err, failed)
	}
	// Plant a marker sender to prove the memo is what Sender answers.
	marker := types.HexToAddress("0x00000000000000000000000000000000000000aa")
	planted := &store.Senders{Signer: signer, Addrs: append([]types.Address(nil), ss.Addrs...)}
	planted.Addrs[0] = marker
	planted.Addrs[1] = types.Address{} // not recovered: left to execution

	txs, _, err := frame.DecodeTxs(raws[0])
	if err != nil {
		t.Fatal(err)
	}
	if n := frame.ApplySenders(txs, planted); n != 3 {
		t.Fatalf("attached %d, want 3", n)
	}
	if got, _ := transaction.Sender(signer, txs[0]); got != marker {
		t.Fatalf("memo not used under its own signer: %s", got)
	}
	if got, _ := transaction.Sender(signer, txs[1]); got != ss.Addrs[1] {
		t.Fatalf("missing entry not recovered: %s", got)
	}
	// A different signer (another chain id) does not trust the memo.
	other := transaction.LatestSignerForChainID(big.NewInt(1))
	if got, err := transaction.Sender(other, txs[0]); err == nil && got == marker {
		t.Fatal("memo leaked to a different signer")
	}
	// Misaligned or empty caches attach nothing.
	fresh, _, _ := frame.DecodeTxs(raws[0])
	if n := frame.ApplySenders(fresh[:2], planted); n != 0 {
		t.Fatalf("misaligned cache attached %d", n)
	}
	if n := frame.ApplySenders(fresh, nil); n != 0 {
		t.Fatalf("nil cache attached %d", n)
	}
}

// TestSenderRecovererSkipsGoneFrames: a frame that leaves the store before
// its turn is not recovered and the pool keeps going.
func TestSenderRecovererSkipsGoneFrames(t *testing.T) {
	keys := frametest.Keys(t, 2, 13)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 2, 5)
	signer := transaction.LatestSignerForChainID(recoverChainID)
	st := store.New(1 << 24)
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 1)
	// Submit directly without starting: nothing is recovered yet.
	st.Put(hashes[0], raws[0])
	rec.Submit(hashes[0], raws[0])
	st.Remove(hashes[:1])
	rec.Start(context.Background())
	defer rec.Stop()
	st.Put(hashes[1], raws[1])
	waitFor(t, "second frame", func() bool { return rec.Recovered() == 1 })
	waitFor(t, "drain", func() bool { return rec.Pending() == 0 })
	if s := rec.Stats(); s.Gone != 1 || s.Frames != 1 || s.Txs != 5 {
		t.Fatalf("stats %+v", s)
	}
}

// TestSenderRecovererSkipsClaimedFrames (P7): a frame a block has already
// resolved is recovered by that block's import, so admission recovery skips
// it instead of recovering it twice; unclaimed frames are still recovered.
func TestSenderRecovererSkipsClaimedFrames(t *testing.T) {
	keys := frametest.Keys(t, 2, 17)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 3, 5)
	signer := transaction.LatestSignerForChainID(recoverChainID)
	st := store.New(1 << 24)
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 1)
	// Queued before the workers start; frame 0 is claimed while it waits.
	for i := range raws {
		st.Put(hashes[i], raws[i])
		rec.Submit(hashes[i], raws[i])
	}
	st.Claim(hashes[:1])
	if st.NeedsSenders(hashes[0]) || !st.NeedsSenders(hashes[1]) {
		t.Fatal("NeedsSenders ignores the claim")
	}
	rec.Start(context.Background())
	defer rec.Stop()
	waitFor(t, "drain", func() bool { return rec.Pending() == 0 })
	if s := rec.Stats(); s.Skipped != 1 || s.Frames != 2 {
		t.Fatalf("stats %+v", s)
	}
	if st.Senders(hashes[0]) != nil || st.Senders(hashes[1]) == nil || st.Senders(hashes[2]) == nil {
		t.Fatal("claimed frame recovered, or an unclaimed one skipped")
	}
}
