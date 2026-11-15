// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// pendingSource answers no frame and claims every frame is expected from
// attestations (a node off every committee).
type pendingSource struct {
	mu        sync.Mutex
	recovered []types.Hash
}

func (*pendingSource) Senders(types.Hash, int) []types.Address { return nil }
func (*pendingSource) Pending(types.Hash) bool                 { return true }
func (p *pendingSource) Recovered(h types.Hash, _ *store.Senders) {
	p.mu.Lock()
	p.recovered = append(p.recovered, h)
	p.mu.Unlock()
}

// TestSenderRecovererHurryLocal (N42_FRAME_HELD_WAIT_MS): a frame whose
// senders are expected from attestations is deferred by admission and by
// the ordinary priority lane (Hurry), so a builder held on it gains
// nothing; HurryLocal recovers it on this node, and the store counts it as
// a local readiness.
func TestSenderRecovererHurryLocal(t *testing.T) {
	keys := frametest.Keys(t, 5, 19)
	fb := frametest.NewBuilder(keys[0], recoverChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 3, 12)
	signer := transaction.LatestSignerForChainID(recoverChainID)

	st := store.New(1 << 24)
	src := &pendingSource{}
	rec := frame.NewSenderRecoverer(st, func() transaction.Signer { return signer }, 2)
	rec.SetSource(src)
	rec.Start(context.Background())
	defer rec.Stop()
	for i := range raws {
		if st.Admit(hashes[i], store.Entry{Raw: raws[i]}, false) != store.Stored {
			t.Fatalf("frame %d not stored", i)
		}
	}
	waitFor(t, "admission jobs", func() bool { return rec.Pending() == 0 })
	if st.HasSenders(hashes[0]) || rec.Stats().Deferred != 3 {
		t.Fatalf("admission recovered a deferred frame (deferred %d)", rec.Stats().Deferred)
	}
	// The ordinary priority lane defers it again.
	if n := st.Hurry(hashes[:1]); n != 1 {
		t.Fatalf("hurry took %d", n)
	}
	waitFor(t, "hurry job", func() bool { return rec.Pending() == 0 })
	if st.HasSenders(hashes[0]) {
		t.Fatal("ordinary hurry recovered a deferred frame")
	}
	// The local lane recovers it here.
	if n := st.HurryLocal(hashes[:2]); n != 2 {
		t.Fatalf("hurry local took %d", n)
	}
	waitFor(t, "local recovery", func() bool { return st.HasSenders(hashes[0]) && st.HasSenders(hashes[1]) })
	if st.HasSenders(hashes[2]) {
		t.Fatal("a frame not hurried was recovered")
	}
	if q, r := rec.HurriedLocal(); q != 2 || r != 2 {
		t.Fatalf("hurried local %d recovered %d", q, r)
	}
	ss := st.Senders(hashes[0])
	if len(ss.Addrs) != 12 || ss.Signer == nil {
		t.Fatalf("senders %+v", ss)
	}
	// A recovered frame is not hurried again.
	if n := st.HurryLocal(hashes[:2]); n != 0 {
		t.Fatalf("hurry local re-took %d recovered frames", n)
	}
	var local uint64
	for _, n := range st.Stats().ReadyLocal {
		local += n
	}
	if local != 2 {
		t.Fatalf("local readiness count %d", local)
	}
	info := st.RecoverInfoOf(hashes[0])
	if !info.Stored || !info.Ready || info.Running || info.Age <= 0 || info.Age > time.Minute {
		t.Fatalf("recover info %+v", info)
	}
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.recovered) != 2 {
		t.Fatalf("source told of %d recoveries", len(src.recovered))
	}
}
