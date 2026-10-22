// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/streamverify"
	"github.com/n42blockchain/N42/modules/state"
)

// mpSeal reproduces what the HotStuff seal does to a header: the trailing
// reserved bytes of Extra are filled in place with the signature, which changes
// the header hash (Header.Hash() covers all of Extra).
func mpSeal(t *testing.T, unsealed block.IBlock) *block.Block {
	t.Helper()
	hdr, ok := unsealed.Header().(*block.Header)
	if !ok {
		t.Fatalf("header is %T", unsealed.Header())
	}
	sealed := block.CopyHeader(hdr)
	extra := make([]byte, 40+96) // vanity/view area plus the reserved 96-byte seal
	copy(extra, sealed.Extra)
	for i := 40; i < len(extra); i++ {
		extra[i] = 0xAB
	}
	sealed.Extra = extra
	sealed.ResetHashCache()
	b, ok := unsealed.(*block.Block)
	if !ok {
		t.Fatalf("block is %T", unsealed)
	}
	return b.WithSeal(sealed)
}

func mpWaitPacket(t *testing.T, ch <-chan *evmsdk.StreamPacket) *evmsdk.StreamPacket {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no mobile packet was delivered")
		return nil
	}
}

// The packet must carry, and be keyed by, the hash of the SEALED block, and the
// header inside it must be the sealed header.
func TestMobilePacketJobBuildsFromSealedHeader(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase
	tk := mnTSealOneBlock(t, w, f)

	job := newMobilePacketJob(tk.block.Transactions(), streamverify.NewReadLogRecorder(nil, nil))
	unsealedHash := tk.block.Hash()
	sealed := mpSeal(t, tk.block) // WithSeal mutates in place, as the real Seal does
	if sealed.Hash() == unsealedHash {
		t.Fatal("test setup: sealing did not change the hash")
	}

	pkt, num, err := job.buildSealed(sealed)
	if err != nil {
		t.Fatalf("buildSealed: %v", err)
	}
	if pkt.BlockHash != sealed.Hash() {
		t.Fatalf("packet keyed by %x, want the sealed hash %x (unsealed %x)", pkt.BlockHash, sealed.Hash(), unsealedHash)
	}
	if num != sealed.Number64().Uint64() {
		t.Fatalf("packet number %d, want %d", num, sealed.Number64().Uint64())
	}
	var inner block.Header
	if err := inner.Unmarshal(pkt.HeaderRLP); err != nil {
		t.Fatalf("decode packet header: %v", err)
	}
	if inner.Hash() != sealed.Hash() {
		t.Fatalf("header inside the packet hashes to %x, want the sealed hash %x", inner.Hash(), sealed.Hash())
	}
}

// launch delivers once, and a second launch of the same job is ignored.
func TestMobilePacketJobLaunchesOnce(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase
	tk := mnTSealOneBlock(t, w, f)
	sealed := mpSeal(t, tk.block)

	var calls atomic.Int32
	got := make(chan *evmsdk.StreamPacket, 4)
	sink := func(p *evmsdk.StreamPacket, _ uint64) { calls.Add(1); got <- p }

	job := newMobilePacketJob(tk.block.Transactions(), streamverify.NewReadLogRecorder(nil, nil))
	job.launch(sealed, sink)
	job.launch(sealed, sink)
	mpWaitPacket(t, got)
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("sink called %d times, want 1", n)
	}

	// Nil job and nil sink are no-ops.
	var nilJob *mobilePacketJob
	nilJob.launch(sealed, sink)
	newMobilePacketJob(nil, streamverify.NewReadLogRecorder(nil, nil)).launch(sealed, nil)
	if newMobilePacketJob(nil, nil) != nil {
		t.Fatal("a job without a recorder must be nil")
	}
}

// The whole path: commit attaches the job to the task, the seal changes the
// hash, handleSealed stores the block and the packet that reaches the sink is
// the SEALED block's.
func TestMobilePacketIsKeyedBySealedHashThroughHandleSealed(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase

	var mu sync.Mutex
	got := make(chan *evmsdk.StreamPacket, 4)
	w.mobilePacketSink = func(p *evmsdk.StreamPacket, _ uint64) {
		mu.Lock()
		defer mu.Unlock()
		got <- p
	}

	atomic.StoreInt32(&w.running, 1)
	env, ibs := mnTBuildFilledEnv(t, w, f)
	rec := streamverify.NewReadLogRecorder(nil, nil)
	if err := w.commit(env, state.NewNoopWriter(), ibs, time.Now(), nil, nil, rec, false, types.Hash{}, time.Time{}, time.Time{}, 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("commit did not send a task")
	}
	if tk.mobilePacket == nil {
		t.Fatal("commit did not attach the packet job to the task")
	}
	select {
	case p := <-got:
		t.Fatalf("a packet was delivered at assembly time, before the seal (key %x)", p.BlockHash)
	case <-time.After(150 * time.Millisecond):
	}

	unsealedHash := tk.block.Hash()
	sealed := mpSeal(t, tk.block)
	if sealed.Hash() == unsealedHash {
		t.Fatal("test setup: sealing did not change the hash")
	}
	w.mu.Lock()
	w.pendingTasks[w.engine.SealHash(sealed.Header())] = tk
	w.mu.Unlock()
	before := f.Chain.CurrentBlock().Number64().Uint64()
	w.handleSealed(sealed)
	if f.Chain.CurrentBlock().Number64().Uint64() != before+1 {
		t.Fatalf("the sealed block was not stored")
	}

	pkt := mpWaitPacket(t, got)
	if pkt.BlockHash != sealed.Hash() {
		t.Fatalf("packet keyed by %x, want the sealed hash %x (unsealed %x)", pkt.BlockHash, sealed.Hash(), unsealedHash)
	}
	var inner block.Header
	if err := inner.Unmarshal(pkt.HeaderRLP); err != nil {
		t.Fatalf("decode packet header: %v", err)
	}
	if inner.Hash() != sealed.Hash() {
		t.Fatalf("header inside the packet hashes to %x, want %x", inner.Hash(), sealed.Hash())
	}
}
