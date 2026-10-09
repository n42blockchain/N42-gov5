package main

import (
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/internal/ingest"
)

type nopPool struct{}

func (nopPool) AddLocal(*transaction.Transaction) error { return nil }
func (nopPool) Stats() (int, int, int, int)             { return 0, 0, 0, 0 }

// TestFrameRunDeliversEveryTx sends 1,234 transactions as frames to an
// in-process ingest listener and checks they arrive as three frames (500,
// 500, 234) carrying every transaction exactly once.
func TestFrameRunDeliversEveryTx(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	st := store.New(16 << 20)
	srv := ingest.NewServer("127.0.0.1:0", nopPool{}, 1<<30, 1<<30)
	if err := srv.EnableFrames(ingest.FrameConfig{Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)}, Store: st}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	const total = 1234
	fr := &frameRun{
		peers: []string{srv.Addr()},
		key:   gw,
		conc:  2,
		total: total,
		rawAt: func(i int64) []byte { return []byte{0xAB, byte(i), byte(i >> 8)} },
	}
	res := fr.run(false)
	if res.Frames != 3 || res.Txs != total || res.Failed != 0 {
		t.Fatalf("result %s", res)
	}
	if st.Len() != 3 {
		t.Fatalf("store holds %d frames", st.Len())
	}
	if got := srv.FrameStats().TxsAccepted; got != total {
		t.Fatalf("server accepted %d txs", got)
	}

	// A key the node does not allow is rejected, never retried forever.
	other, _ := crypto.GenerateKey()
	bad := &frameRun{peers: []string{srv.Addr()}, key: other, conc: 1, total: frame.MaxTxs,
		rawAt: func(i int64) []byte { return []byte{1, byte(i)} }}
	if res := bad.run(false); res.Failed != frame.MaxTxs || res.Frames != 0 {
		t.Fatalf("unknown gateway: %s", res)
	}
}

// TestFrameRunBroadcast: with broadcast every frame reaches every peer and
// is counted once.
func TestFrameRunBroadcast(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	var peers []string
	var stores []*store.Store
	for i := 0; i < 3; i++ {
		st := store.New(16 << 20)
		srv := ingest.NewServer("127.0.0.1:0", nopPool{}, 1<<30, 1<<30)
		if err := srv.EnableFrames(ingest.FrameConfig{Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)}, Store: st}); err != nil {
			t.Fatal(err)
		}
		if err := srv.Start(); err != nil {
			t.Fatal(err)
		}
		defer srv.Stop()
		peers = append(peers, srv.Addr())
		stores = append(stores, st)
	}
	fr := &frameRun{peers: peers, key: gw, conc: 2, total: 1600, broadcast: true,
		rawAt: func(i int64) []byte { return []byte{0xCD, byte(i), byte(i >> 8)} }}
	if res := fr.run(false); res.Frames != 4 || res.Txs != 1600 || res.Failed != 0 {
		t.Fatalf("result %s", res)
	}
	for i, st := range stores {
		if st.Len() != 4 {
			t.Fatalf("peer %d holds %d frames", i, st.Len())
		}
	}
}
