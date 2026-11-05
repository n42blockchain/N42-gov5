// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// orphanFixture is run 36's B2 shape: committed head H-2, parent P at H-1
// (sealed here, not committed) holding frames 0-1, and proposal A at H on P
// holding frames 2-3, resolved and written here (in flight and pinned),
// then abandoned: the next leader builds sibling B at H on P.
type orphanFixture struct {
	st     *store.Store
	hashes []types.Hash
	bc     *internal.BlockChain
	w      *worker
	parent block.IBlock
}

const orphanH = 40

func newOrphanFixture(t *testing.T) *orphanFixture {
	st, hashes := frameStoreSenders(t, 6, 4, 3)
	bc := &internal.BlockChain{}
	bc.SetFrameStore(st)
	st.Commit(orphanH-2, nil)
	mk := func(n uint64, parent types.Hash, extra byte, idx ...int) block.IBlock {
		hdr := &block.Header{Number: uint256.NewInt(n), ParentHash: parent, Difficulty: uint256.NewInt(0),
			BaseFee: uint256.NewInt(1), GasLimit: 1 << 30, Extra: []byte{extra}}
		var hs []types.Hash
		for _, i := range idx {
			hs = append(hs, hashes[i])
		}
		return block.NewFrameBlockFromReceipt(hdr, hs, nil, nil, nil)
	}
	p := mk(orphanH-1, types.Hash{0x01}, 1, 0, 1)
	a := mk(orphanH, p.Hash(), 2, 2, 3)
	w := &worker{chain: bc, sealedByHash: map[types.Hash]block.IBlock{p.Hash(): p}}
	pf := p.Body().(*block.Body).FrameHashes
	af := a.Body().(*block.Body).FrameHashes
	bc.MarkFramesInFlight(pf, orphanH-1)
	st.Include(pf, orphanH-1)
	bc.MarkFramesInFlight(af, orphanH)
	st.Include(af, orphanH)
	return &orphanFixture{st: st, hashes: hashes, bc: bc, w: w, parent: p}
}

// pickB is the gateway pick of sibling B at H on P.
func (f *orphanFixture) pickB() framePick {
	return f.w.pickGatewayFrames(f.bc, nil, f.parent.Hash(), orphanH-1, f.st, f.bc.FramesInFlight(), types.Address{}, 1<<62, 1024)
}

// requireNonceContiguous decodes the chain's frames in order and requires
// each sender's nonces to follow on from its first one: no transaction of
// the chain can be a future nonce.
func requireNonceContiguous(t *testing.T, st *store.Store, chain []types.Hash) {
	t.Helper()
	next := map[types.Address]uint64{}
	for _, h := range chain {
		raw, ok := st.Get(h)
		if !ok {
			t.Fatalf("frame %x not stored", h[:4])
		}
		txs, _, err := frame.DecodeTxs(raw)
		if err != nil {
			t.Fatal(err)
		}
		for _, tx := range txs {
			from, err := transaction.Sender(frameTestSigner, tx)
			if err != nil {
				t.Fatal(err)
			}
			n, seen := next[from]
			if seen && tx.Nonce() != n {
				t.Fatalf("sender %x: nonce %d after %d (a gap: the chain would skip it as a future nonce)", from[:4], tx.Nonce(), n-1)
			}
			next[from] = tx.Nonce() + 1
		}
	}
}

// TestOrphanReinjectSiblingTakesLostFrames (N42_ORPHAN_REINJECT): with the
// switch off, B passes over A's in-flight and pinned frames and takes the
// later ones, whose nonces follow A's -- the gap run 36 hit. With it on, B
// takes A's frames first, in arrival order, never P's, and the chain P, B
// has contiguous nonces.
func TestOrphanReinjectSiblingTakesLostFrames(t *testing.T) {
	t.Run("off", func(t *testing.T) {
		defer internal.SetOrphanReinjectForTest(false)()
		f := newOrphanFixture(t)
		p := f.pickB()
		if len(p.hashes) != 2 || p.hashes[0] != f.hashes[4] || p.hashes[1] != f.hashes[5] {
			t.Fatalf("off: picked %d frames, want the two after A's", len(p.hashes))
		}
		chain := append([]types.Hash{f.hashes[0], f.hashes[1]}, p.hashes...)
		if contiguous(f.st, chain) {
			t.Fatal("off: expected the run-36 nonce gap")
		}
		// The fixture's frames themselves are contiguous in arrival order.
		requireNonceContiguous(t, f.st, f.hashes)
	})
	t.Run("on", func(t *testing.T) {
		defer internal.SetOrphanReinjectForTest(true)()
		f := newOrphanFixture(t)
		p := f.pickB()
		want := []types.Hash{f.hashes[2], f.hashes[3], f.hashes[4], f.hashes[5]}
		if len(p.hashes) != len(want) {
			t.Fatalf("on: picked %d frames, want %d", len(p.hashes), len(want))
		}
		for i := range want {
			if p.hashes[i] != want[i] {
				t.Fatalf("on: position %d is not A's frame order", i)
			}
		}
		requireNonceContiguous(t, f.st, append([]types.Hash{f.hashes[0], f.hashes[1]}, p.hashes...))
		// B written and committed at H: A's frames are B's now; the next
		// block on B takes nothing of the chain again.
		bf := p.hashes
		f.st.Include(bf, orphanH)
		f.st.Commit(orphanH-1, []types.Hash{f.hashes[0], f.hashes[1]})
		f.st.Commit(orphanH, bf)
		b := block.NewFrameBlockFromReceipt(&block.Header{Number: uint256.NewInt(orphanH), ParentHash: f.parent.Hash(),
			Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1), GasLimit: 1 << 30}, bf, nil, nil, nil)
		f.w.sealedByHash[b.Hash()] = b
		next := f.w.pickGatewayFrames(f.bc, nil, b.Hash(), orphanH, f.st, f.bc.FramesInFlight(), types.Address{}, 1<<62, 1024)
		if len(next.hashes) != 0 {
			t.Fatalf("on: the child of B took %d frames already on the chain", len(next.hashes))
		}
	})
	t.Run("onUnknownAncestry", func(t *testing.T) {
		// The parent is not known here: the pick falls back to the pins and
		// the in-flight set, as with the switch off.
		defer internal.SetOrphanReinjectForTest(true)()
		f := newOrphanFixture(t)
		delete(f.w.sealedByHash, f.parent.Hash())
		p := f.pickB()
		if len(p.hashes) != 2 || p.hashes[0] != f.hashes[4] {
			t.Fatalf("fallback: picked %d frames", len(p.hashes))
		}
	})
}

// frameStoreSenders is frameStoreOf with nSenders senders in turn, so each
// sender has transactions in every frame with consecutive nonces across
// frames (a gateway's stream, as in the bench).
func frameStoreSenders(t testing.TB, nFrames, perFrame, nSenders int) (*store.Store, []types.Hash) {
	keys := frametest.Keys(t, nSenders+1, 11)
	fb := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	raws, hashes, _ := fb.Frames(t, nFrames, perFrame)
	st := store.New(1 << 30)
	for i, raw := range raws {
		f, err := frame.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if !st.PutEntry(hashes[i], store.Entry{Raw: raw, Gas: frame.GasEstimate(f), Txs: int(f.TxCount)}) {
			t.Fatal("put")
		}
	}
	return st, hashes
}

// frameTestSigner is frameStoreOf's chain (id 94).
var frameTestSigner = transaction.LatestSignerForChainID(big.NewInt(94))

// contiguous reports whether the chain's frames have contiguous nonces per
// sender.
func contiguous(st *store.Store, chain []types.Hash) bool {
	next := map[types.Address]uint64{}
	for _, h := range chain {
		raw, _ := st.Get(h)
		txs, _, err := frame.DecodeTxs(raw)
		if err != nil {
			return false
		}
		for _, tx := range txs {
			from, err := transaction.Sender(frameTestSigner, tx)
			if err != nil {
				return false
			}
			if n, seen := next[from]; seen && tx.Nonce() != n {
				return false
			}
			next[from] = tx.Nonce() + 1
		}
	}
	return true
}

