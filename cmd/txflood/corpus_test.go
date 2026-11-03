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

// TestPresignReuse checks that -presign-reuse takes a corpus only when it was
// built for exactly this run: same shape, gateway, start nonces and signed
// transactions, written in full (v2, renamed into place).
func TestPresignReuse(t *testing.T) {
	_, st, stop, sink := sinkServer(t)
	defer stop()
	gw := sink.key
	newRun := func(rawAt func(int64) []byte, peers int, nonces []uint64) *frameRun {
		fr := &frameRun{peers: make([]string, peers), key: gw, total: 1234, rawAt: rawAt, workers: 3}
		fr.fingerprint = fr.corpusFingerprint(nonces)
		return fr
	}
	signed := signedRawAt()
	nonces := []uint64{0, 0, 5}
	dir := t.TempDir()
	p := filepath.Join(dir, "corpus-0.bin")

	base := newRun(signed, 1, nonces)
	if ok, _ := base.reusableCorpus(p); ok {
		t.Fatal("missing file reported reusable")
	}
	if err := base.writeCorpus(p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file left behind: %v", err)
	}

	// Same inputs, fresh signer closure: deterministic signatures give the
	// same fingerprint and a byte-identical corpus.
	again := newRun(signedRawAt(), 1, nonces)
	if again.fingerprint != base.fingerprint {
		t.Fatal("fingerprint not deterministic")
	}
	if ok, why := again.reusableCorpus(p); !ok {
		t.Fatalf("identical run not reusable: %s", why)
	}
	p2 := filepath.Join(dir, "corpus-1.bin")
	if err := again.writeCorpus(p2); err != nil {
		t.Fatal(err)
	}
	b1, _ := os.ReadFile(p)
	b2, _ := os.ReadFile(p2)
	if len(b1) == 0 || string(b1) != string(b2) {
		t.Fatalf("corpus bytes differ: %d vs %d", len(b1), len(b2))
	}

	// Any input that changes the transactions or the frames must miss.
	otherTx := func(i int64) []byte {
		b := signed(i)
		if i == 1233 { // the last sample
			b = append([]byte{}, b...)
			b[len(b)-1] ^= 1
		}
		return b
	}
	for name, fr := range map[string]*frameRun{
		"start nonce": newRun(signed, 1, []uint64{0, 1, 5}),
		"peers":       newRun(signed, 2, nonces),
		"last tx":     newRun(otherTx, 1, nonces),
	} {
		if ok, _ := fr.reusableCorpus(p); ok {
			t.Fatalf("%s change not detected", name)
		}
	}
	gw2 := newRun(signed, 1, nonces)
	gw2.key = deriveKey(8)
	gw2.fingerprint = gw2.corpusFingerprint(nonces)
	if ok, _ := gw2.reusableCorpus(p); ok {
		t.Fatal("gateway change not detected")
	}

	// A v1 corpus (no fingerprint) still replays but is never reused.
	v1 := filepath.Join(dir, "v1.bin")
	if err := os.WriteFile(v1, append([]byte(corpusMagicV1), b1[len(corpusMagic):len(corpusMagic)+21]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := openCorpus(v1); err != nil || c.fp != nil {
		t.Fatalf("v1 open: %v", err)
	} else {
		c.f.Close()
	}
	if ok, _ := base.reusableCorpus(v1); ok {
		t.Fatal("v1 corpus reported reusable")
	}

	// The reused file replays every transaction to the sink.
	c, err := openCorpus(p)
	if err != nil {
		t.Fatal(err)
	}
	sink.total, sink.corpus = 1234, c
	res := sink.run(false)
	if res.Txs != 1234 || res.Failed != 0 || st.Len() != 3 {
		t.Fatalf("replay: %s", res)
	}
}
