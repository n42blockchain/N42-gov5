// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package attest

import (
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// TestStageStamps (N42_FRAME_ATTEST_STAMPS): with the stamps on, a member's
// recovery, its batch wait and signing, and the receiver's arrival and
// application are timed; off, nothing is and the line carries no stages.
func TestStageStamps(t *testing.T) {
	require := func(cond bool, f string, a ...any) {
		t.Helper()
		if !cond {
			t.Fatalf(f, a...)
		}
	}
	restore := SetStampsForTest(false)
	require(StagesString() == "", "stages rendered with the stamps off")
	restore()
	defer SetStampsForTest(true)()
	fl := newFleet(t, 7, 2)
	h := randHash(t)
	m := fl.members(h, 3)
	st := store.New(1 << 20)
	require(st.Admit(h, store.Entry{Raw: []byte{1}}, true) == store.Stored, "not stored")
	var sent [][]byte
	pub := NewPool(Config{Validators: func() *Validators { return fl.v }, Self: func() types.Address { return m[1] },
		Sign:          func(msg []byte) ([]byte, error) { return fl.keys[m[1]].Sign(msg).Marshal(), nil },
		Send:          func(msg []byte) { sent = append(sent, msg) },
		Store:         st,
		CommitteeSize: 3})
	r0, b0, s0 := stRecover.n.Load(), stBatch.n.Load(), stSign.n.Load()
	pub.Recovered(h, &store.Senders{Addrs: randAddrs(t, 5)})
	time.Sleep(2 * time.Millisecond)
	pub.Flush()
	require(len(sent) == 1, "%d messages sent", len(sent))
	require(stRecover.n.Load() == r0+1 && stBatch.n.Load() == b0+1 && stSign.n.Load() == s0+1,
		"attester stages not observed: %s", StagesString())
	require(stBatch.maxUs.Load() >= 2000, "batch wait %d us, want >= 2 ms", stBatch.maxUs.Load())

	rx := newTestPool(fl, fl.outsider(h, 3), nil)
	rx.cfg.Store = st
	v0, e0, a0 := stRecv.n.Load(), stRecvEarly.Load(), stApply.n.Load()
	rx.HandleWire(sent[0][8:])
	require(stRecv.n.Load() == v0+1 && stApply.n.Load() == a0+1, "receiver stages not observed: %s", StagesString())
	// An entry for a frame not admitted here counts as early.
	h2 := randHash(t)
	rx.cfg.Store = store.New(1 << 20)
	_ = rx.Add(fl.batch(fl.members(h2, 3)[0], Entry{Frame: h2, Digest: Digest(randAddrs(t, 2))}))
	require(stRecvEarly.Load() == e0+1, "early entry not counted")
	require(strings.Contains(StagesString(), "recover="), "stages %q", StagesString())
}
