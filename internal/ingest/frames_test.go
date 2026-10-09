// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package ingest

import (
	"crypto/ecdsa"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

var testChainID = big.NewInt(94)

func testSigner() transaction.Signer { return transaction.LatestSignerForChainID(testChainID) }

// execPool models the pool's executability rule: a sender's transactions are
// executable while their nonces run contiguously from zero, whatever order
// they arrived in.
type execPool struct {
	mu     sync.Mutex
	nonces map[types.Address]map[uint64]bool
	calls  int
	noFrom int
}

func newExecPool() *execPool { return &execPool{nonces: map[types.Address]map[uint64]bool{}} }

func (p *execPool) AddLocal(tx *transaction.Transaction) error {
	return p.AddLocals([]*transaction.Transaction{tx})[0]
}

func (p *execPool) AddLocals(txs []*transaction.Transaction) []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	for _, tx := range txs {
		from := tx.From()
		if from == nil {
			p.noFrom++
			continue
		}
		m := p.nonces[*from]
		if m == nil {
			m = map[uint64]bool{}
			p.nonces[*from] = m
		}
		m[tx.Nonce()] = true
	}
	return make([]error, len(txs))
}

func (p *execPool) Stats() (int, int, int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.executable(), 0, 0, 0
}

// executable must be called with p.mu held.

func (p *execPool) executable() int {
	n := 0
	for _, m := range p.nonces {
		for i := uint64(0); m[i]; i++ {
			n++
		}
	}
	return n
}

func (p *execPool) senders() map[types.Address]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[types.Address]bool{}
	for a := range p.nonces {
		out[a] = true
	}
	return out
}

// signedTxs signs senders x perSender transfers in interleaved order
// (sender i%senders, nonce i/senders), the order txflood -interleave sends.
func signedTxs(t testing.TB, senders, perSender int) ([][]byte, map[types.Address]bool) {
	t.Helper()
	signer := testSigner()
	keys := make([]*ecdsa.PrivateKey, senders)
	addrs := map[types.Address]bool{}
	for i := range keys {
		keys[i], _ = crypto.GenerateKey()
		addrs[crypto.PubkeyToAddress(keys[i].PublicKey)] = true
	}
	to := types.HexToAddress("0x000000000000000000000000000000000000dEaD")
	out := make([][]byte, senders*perSender)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < len(out); i += 8 {
				s, n := i%senders, uint64(i/senders)
				inner := &transaction.LegacyTx{Nonce: n, GasPrice: uint256.NewInt(1e9), Gas: 21000, To: &to, Value: uint256.NewInt(1)}
				tx, err := transaction.SignTx(transaction.NewTx(inner), signer, keys[s])
				if err != nil {
					t.Error(err)
					return
				}
				raw, err := transaction.EncodeEthereumTransaction(tx)
				if err != nil {
					t.Error(err)
					return
				}
				out[i] = raw
			}
		}(w)
	}
	wg.Wait()
	return out, addrs
}

func packFrames(t testing.TB, key *ecdsa.PrivateKey, txs [][]byte) [][]byte {
	t.Helper()
	var frames [][]byte
	for seq := 0; len(txs) > 0; seq++ {
		n := frame.MaxTxs
		if n > len(txs) {
			n = len(txs)
		}
		raw, _, err := frame.Encode(key, uint64(seq), frame.TxTypeEthereum, txs[:n])
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, raw)
		txs = txs[n:]
	}
	return frames
}

func sendFrame(t testing.TB, conn net.Conn, raw []byte) (byte, types.Hash) {
	t.Helper()
	var magic [4]byte
	binary.LittleEndian.PutUint32(magic[:], FrameMagic)
	if _, err := conn.Write(append(magic[:], raw...)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	var reply [FrameReplySize]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var h types.Hash
	copy(h[:], reply[1:])
	return reply[0], h
}

func startFrameServer(t testing.TB, pool *execPool, cfg FrameConfig) *Server {
	t.Helper()
	srv := NewServer("127.0.0.1:0", pool, 1<<30, 1<<30)
	if pool != nil {
		cfg.Pool = pool
	}
	if err := srv.EnableFrames(cfg); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)
	return srv
}

func waitFor(t testing.TB, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestFrameIngestEndToEnd sends 10 frames x 500 interleaved transactions
// over one connection and checks that the pool ends up holding 5000
// executable transactions with signature-recovered senders, and that no
// frame was ever answered busy (the accept path never waited on the bridge).
func TestFrameIngestEndToEnd(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	txs, senders := signedTxs(t, 50, 100)
	frames := packFrames(t, gw, txs)
	if len(frames) != 10 {
		t.Fatalf("packed %d frames", len(frames))
	}
	pool := newExecPool()
	srv := startFrameServer(t, pool, FrameConfig{
		Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)},
		Store:    store.New(64 << 20),
		Signer:   testSigner,
		Workers:  8,
	})
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	start := time.Now()
	for i, raw := range frames {
		status, h := sendFrame(t, conn, raw)
		if status != FrameAccepted {
			t.Fatalf("frame %d: status %d", i, status)
		}
		if h != crypto.Keccak256Hash(raw[:len(raw)-frame.SigSize]) {
			t.Fatalf("frame %d: ack hash mismatch", i)
		}
	}
	ingest := time.Since(start)
	waitFor(t, 30*time.Second, func() bool { return srv.FrameStats().BridgeFrames == 10 })
	bridge := time.Since(start)

	pool.mu.Lock()
	exec, noFrom, calls := pool.executable(), pool.noFrom, pool.calls
	pool.mu.Unlock()
	if exec != 5000 || noFrom != 0 {
		t.Fatalf("pool: executable=%d noFrom=%d", exec, noFrom)
	}
	if calls != 10 {
		t.Fatalf("pool saw %d AddLocals calls, want one per frame", calls)
	}
	for a := range pool.senders() {
		if !senders[a] {
			t.Fatalf("unexpected sender %s", a)
		}
	}
	st := srv.FrameStats()
	if st.Accepted != 10 || st.TxsAccepted != 5000 || st.Busy != 0 || st.Bridged != 5000 || st.Store.Frames != 10 {
		t.Fatalf("stats %+v", st)
	}
	// Re-sending a frame is a duplicate, not a second pool insert.
	if status, _ := sendFrame(t, conn, frames[0]); status != FrameDuplicate {
		t.Fatalf("resend status %d", status)
	}
	t.Logf("ingest: 10 frames / 5000 txs acked in %v (%.0f frames/s, %.0f tx/s); bridged into pool in %v (%.0f tx/s)",
		ingest, 10/ingest.Seconds(), 5000/ingest.Seconds(), bridge, 5000/bridge.Seconds())
}

// blockingPool blocks every AddLocals until release is closed.
type blockingPool struct {
	release chan struct{}
	entered atomic.Int32
}

func (p *blockingPool) AddLocal(*transaction.Transaction) error { return nil }
func (p *blockingPool) Stats() (int, int, int, int)             { return 0, 0, 0, 0 }
func (p *blockingPool) AddLocals(txs []*transaction.Transaction) []error {
	p.entered.Add(1)
	<-p.release
	return make([]error, len(txs))
}

// TestFrameIngestNeverWaitsOnBridge wedges the pool and checks that every
// frame is still answered promptly: the bridge's queue fills, then further
// frames are answered FrameBusy instead of blocking the connection.
func TestFrameIngestNeverWaitsOnBridge(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	txs, _ := signedTxs(t, 6, 1)
	pool := &blockingPool{release: make(chan struct{})}
	srv := NewServer("127.0.0.1:0", pool, 1<<30, 1<<30)
	if err := srv.EnableFrames(FrameConfig{
		Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)},
		Store:    store.New(1 << 20),
		Pool:     pool,
		Signer:   testSigner,
		Workers:  1,
		Queue:    1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	defer close(pool.release)

	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var accepted, busy int
	for i := 0; i < 6; i++ {
		raw, _, _ := frame.Encode(gw, uint64(i), frame.TxTypeEthereum, txs[i:i+1])
		status, _ := sendFrame(t, conn, raw)
		switch status {
		case FrameAccepted:
			accepted++
		case FrameBusy:
			busy++
		default:
			t.Fatalf("frame %d: status %d", i, status)
		}
		if i == 0 {
			// Let the single worker pick the first frame up and wedge.
			waitFor(t, 5*time.Second, func() bool { return pool.entered.Load() == 1 })
		}
	}
	// One frame wedged in the worker, one in the queue, the rest busy.
	if accepted != 2 || busy != 4 {
		t.Fatalf("accepted=%d busy=%d", accepted, busy)
	}
	// Busy frames were not stored, so a retry is not a duplicate.
	if srv.FrameStore().Len() != 2 {
		t.Fatalf("store holds %d frames", srv.FrameStore().Len())
	}
}

func TestFrameIngestRejects(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	stranger, _ := crypto.GenerateKey()
	txs, _ := signedTxs(t, 2, 1)
	pool := newExecPool()
	srv := startFrameServer(t, pool, FrameConfig{
		Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)},
		Store:    store.New(1 << 20),
		Signer:   testSigner,
	})
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Unknown gateway.
	raw, _, _ := frame.Encode(stranger, 0, frame.TxTypeEthereum, txs)
	if status, _ := sendFrame(t, conn, raw); status != FrameUnknownGateway {
		t.Fatalf("stranger: status %d", status)
	}
	// Allowlisted gateway, signature by someone else.
	raw, _, _ = frame.Encode(stranger, 0, frame.TxTypeEthereum, txs)
	a := crypto.PubkeyToAddress(gw.PublicKey)
	copy(raw[2:22], a[:])
	if status, _ := sendFrame(t, conn, raw); status != FrameBadSignature {
		t.Fatalf("forged: status %d", status)
	}
	// Same connection still carries a legacy batch afterwards.
	if got := writeBatch(t, conn, makeTxBytes(t), 2); got != 2 {
		t.Fatalf("legacy batch after frames: %d", got)
	}
	// And a valid frame.
	raw, _, _ = frame.Encode(gw, 1, frame.TxTypeEthereum, txs)
	if status, _ := sendFrame(t, conn, raw); status != FrameAccepted {
		t.Fatalf("valid: status %d", status)
	}
	st := srv.FrameStats()
	if st.UnknownGateway != 1 || st.BadSig != 1 || st.Accepted != 1 || st.Store.Frames != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestFrameIngestDisabled(t *testing.T) {
	gw, _ := crypto.GenerateKey()
	txs, _ := signedTxs(t, 1, 1)
	srv := NewServer("127.0.0.1:0", &mockTxPool{}, 1<<30, 1<<30)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	raw, _, _ := frame.Encode(gw, 0, frame.TxTypeEthereum, txs)
	if status, _ := sendFrame(t, conn, raw); status != FrameDisabled {
		t.Fatalf("status %d", status)
	}
}

func TestParseGateways(t *testing.T) {
	g, err := ParseGateways(" 0x000000000000000000000000000000000000dEaD, ,0x0000000000000000000000000000000000000001")
	if err != nil || len(g) != 2 || g[1] != types.HexToAddress("0x01") {
		t.Fatalf("%v %v", g, err)
	}
	if _, err := ParseGateways("nope"); err == nil {
		t.Fatal("bad address accepted")
	}
	if g, _ := ParseGateways(""); len(g) != 0 {
		t.Fatal("empty list")
	}
}

// BenchmarkFrameIngest measures the door alone (read, keccak, ecrecover,
// store put) over 8 parallel connections, with the bridge off.
func BenchmarkFrameIngest(b *testing.B) {
	gw, _ := crypto.GenerateKey()
	txs, _ := signedTxs(b, 50, 10)
	srv := NewServer("127.0.0.1:0", &mockTxPool{}, 1<<30, 1<<30)
	if err := srv.EnableFrames(FrameConfig{
		Gateways: []types.Address{crypto.PubkeyToAddress(gw.PublicKey)},
		Store:    store.New(256 << 20),
	}); err != nil {
		b.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		b.Fatal(err)
	}
	defer srv.Stop()
	// Pre-encode so the client's signing stays out of the measurement. Once
	// the set wraps, frames come back FrameDuplicate, which still pays the
	// full read + keccak + ecrecover before the store lookup.
	pre := make([][]byte, 1024)
	for i := range pre {
		pre[i], _, _ = frame.Encode(gw, uint64(i), frame.TxTypeEthereum, txs[:frame.MaxTxs])
	}
	var seq atomic.Uint64
	b.SetParallelism(1)
	b.ResetTimer()
	start := time.Now()
	b.RunParallel(func(pb *testing.PB) {
		conn, err := net.Dial("tcp", srv.Addr())
		if err != nil {
			b.Error(err)
			return
		}
		defer conn.Close()
		for pb.Next() {
			raw := pre[seq.Add(1)%uint64(len(pre))]
			if status, _ := sendFrame(b, conn, raw); status != FrameAccepted && status != FrameDuplicate {
				b.Errorf("status %d", status)
				return
			}
		}
	})
	el := time.Since(start)
	b.ReportMetric(float64(b.N)/el.Seconds(), "frames/s")
	b.ReportMetric(float64(b.N*frame.MaxTxs)/el.Seconds(), "tx/s")
}
