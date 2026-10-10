package main

import (
	"crypto/ecdsa"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/internal/ingest"
)

// signedRawAt returns a rawAt that really signs a 21000-gas transfer from one
// of 64 senders, as the lazy-sign path does.
func signedRawAt() func(i int64) []byte {
	signer := transaction.NewLondonSigner(big.NewInt(94))
	const ns = 64
	keys := make([]*ecdsa.PrivateKey, ns)
	for s := range keys {
		keys[s] = deriveKey(s)
	}
	to := types.HexToAddress("0x000000000000000000000000000000000000dEaD")
	return func(i int64) []byte {
		s := int(i % ns)
		from := crypto.PubkeyToAddress(keys[s].PublicKey)
		inner := &transaction.LegacyTx{Nonce: uint64(i / ns), GasPrice: uint256.NewInt(1000000008), Gas: 21000, To: &to, Value: uint256.NewInt(1), From: &from}
		signed, _ := transaction.SignTx(transaction.NewTx(inner), signer, keys[s])
		raw, _ := transaction.EncodeEthereumTransaction(signed)
		return raw
	}
}

func sinkServer(t testing.TB) (*ingest.Server, *store.Store, func(), *frameRun) {
	gw, _ := crypto.GenerateKey()
	st := store.New(1 << 30)
	srv := ingest.NewServer("127.0.0.1:0", nopPool{}, 1<<30, 1<<30)
	if err := srv.EnableFrames(ingest.FrameConfig{Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)}, Store: st}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	return srv, st, srv.Stop, &frameRun{peers: []string{srv.Addr()}, key: gw, conc: 1}
}

// TestParallelAndCorpusDeliverSame checks that parallel building and a
// presign/replay round trip deliver every transaction in order to the sink.
func TestParallelAndCorpusDeliverSame(t *testing.T) {
	const total = 5321
	for _, mode := range []string{"parallel", "corpus"} {
		srv, st, stop, fr := sinkServer(t)
		fr.total = total
		fr.rawAt = func(i int64) []byte { return []byte{0xAB, byte(i), byte(i >> 8), byte(i >> 16)} }
		fr.workers = 4
		if mode == "corpus" {
			p := filepath.Join(t.TempDir(), "c.bin")
			if err := fr.writeCorpus(p); err != nil {
				t.Fatal(err)
			}
			c, err := openCorpus(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.check(1, false, total); err != nil {
				t.Fatal(err)
			}
			if c.check(2, false, total) == nil {
				t.Fatal("shape mismatch not detected")
			}
			fr.corpus = c
		}
		res := fr.run(false)
		if res.Frames != 11 || res.Txs != total || res.Failed != 0 || st.Len() != 11 {
			t.Fatalf("%s: %s store=%d", mode, res, st.Len())
		}
		if got := srv.FrameStats().TxsAccepted; got != total {
			t.Fatalf("%s: accepted %d", mode, got)
		}
		if mode == "corpus" && fr.streamed.Load() != total {
			t.Fatalf("streamed %d", fr.streamed.Load())
		}
		stop()
	}
}

// TestSupplyRate measures one generator against an in-process ingest sink
// (run with -run TestSupplyRate -v; N42_FLOOD_BENCH=1).
func TestSupplyRate(t *testing.T) {
	if testing.Short() || benchTotal() == 0 {
		t.Skip("set N42_FLOOD_BENCH=<txs>")
	}
	total := benchTotal()
	for _, cfg := range []struct {
		name    string
		workers int
		corpus  bool
	}{{"1-worker live", 1, false}, {"4-worker live", 4, false}, {"presign 4w", 4, true}} {
		_, _, stop, fr := sinkServer(t)
		fr.total = total
		fr.rawAt = signedRawAt()
		fr.workers = cfg.workers
		fr.broadcast = true
		var pre time.Duration
		if cfg.corpus {
			p := filepath.Join(t.TempDir(), "c.bin")
			t0 := time.Now()
			if err := fr.writeCorpus(p); err != nil {
				t.Fatal(err)
			}
			pre = time.Since(t0)
			c, _ := openCorpus(p)
			fr.corpus = c
		}
		res := fr.run(false)
		t.Logf("%-14s %s presign=%s", cfg.name, res, pre.Round(time.Millisecond))
		stop()
	}
}

func benchTotal() int64 {
	n, _ := strconv.ParseInt(os.Getenv("N42_FLOOD_BENCH"), 10, 64)
	return n
}
