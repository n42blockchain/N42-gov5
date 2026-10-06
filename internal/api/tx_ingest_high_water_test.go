package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// fakeTxsPool is a minimal common.ITxsPool for exercising the high-water
// gate without a real pool. statsCalls counts how many times Stats() was
// consulted, so a test can confirm the gate actually asked before deciding.
type fakeTxsPool struct {
	pending, queued int
	statsCalls      int
}

func (f *fakeTxsPool) Stop() error                                               { return nil }
func (f *fakeTxsPool) Has(types.Hash) bool                                       { return false }
func (f *fakeTxsPool) Pending(bool) map[types.Address][]*transaction.Transaction { return nil }
func (f *fakeTxsPool) GetTransaction() ([]*transaction.Transaction, error)       { return nil, nil }
func (f *fakeTxsPool) GetTx(types.Hash) *transaction.Transaction                 { return nil }
func (f *fakeTxsPool) AddRemotes([]*transaction.Transaction) []error             { return nil }
func (f *fakeTxsPool) AddLocal(*transaction.Transaction) error                   { return nil }
func (f *fakeTxsPool) AddLocals(txs []*transaction.Transaction) []error {
	return make([]error, len(txs))
}
func (f *fakeTxsPool) Nonce(types.Address) uint64 { return 0 }
func (f *fakeTxsPool) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	return nil, nil
}
func (f *fakeTxsPool) Stats() (int, int, int, int) {
	f.statsCalls++
	return 0, f.pending, 0, f.queued
}

var _ common.ITxsPool = (*fakeTxsPool)(nil)

func TestRejectAboveHighWaterMark(t *testing.T) {
	cases := []struct {
		name            string
		pending, queued int
		mark            uint64
		want            bool
	}{
		{"below mark", 100, 50, 1000, false},
		{"at mark", 600, 400, 1000, true},
		{"above mark", 700, 400, 1000, true},
		{"mark zero is off even when pool is huge", 999999, 999999, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pool := &fakeTxsPool{pending: c.pending, queued: c.queued}
			got := rejectAboveHighWaterMark(pool, c.mark)
			if got != c.want {
				t.Fatalf("rejectAboveHighWaterMark(pending=%d,queued=%d,mark=%d) = %v, want %v",
					c.pending, c.queued, c.mark, got, c.want)
			}
			if c.mark != 0 && pool.statsCalls != 1 {
				t.Fatalf("Stats() called %d times, want 1", pool.statsCalls)
			}
			if c.mark == 0 && pool.statsCalls != 0 {
				t.Fatalf("Stats() called %d times with the gate off, want 0 (must not even ask)", pool.statsCalls)
			}
		})
	}
}

func TestRejectAboveHighWaterMarkNilPool(t *testing.T) {
	if rejectAboveHighWaterMark(nil, 1) {
		t.Fatal("a nil pool must never be treated as above the mark")
	}
}

// TestSendRawTransactionAboveHighWaterSkipsDecode forces the mark via the
// package-level singleton (a legitimate same-package test seam: the Once is
// spent with a no-op so txIngestHighWater() just returns whatever the test
// pins) and confirms SendRawTransaction returns errAboveHighWater for input
// that is NOT valid transaction RLP. If decode had run first, it would fail
// with an RLP/decode error instead -- getting exactly errAboveHighWater back
// is proof decode never happened.
func TestSendRawTransactionAboveHighWaterSkipsDecode(t *testing.T) {
	txIngestHighWaterOnce.Do(func() {})
	old := txIngestHighWaterMark
	txIngestHighWaterMark = 5
	t.Cleanup(func() { txIngestHighWaterMark = old })

	pool := &fakeTxsPool{pending: 3, queued: 3} // 6 >= 5
	txAPI := NewTransactionAPI(&API{txspool: pool}, new(AddrLocker))

	_, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes{0xde, 0xad})
	if err != errAboveHighWater {
		t.Fatalf("SendRawTransaction error = %v, want errAboveHighWater", err)
	}
	if pool.statsCalls != 1 {
		t.Fatalf("Stats() called %d times, want 1", pool.statsCalls)
	}
}

// TestSendRawTransactionBelowHighWaterFallsThrough confirms the gate does
// not interfere when the pool is under the mark: the same invalid input now
// reaches decode and fails there instead (a different, decode-shaped error).
func TestSendRawTransactionBelowHighWaterFallsThrough(t *testing.T) {
	txIngestHighWaterOnce.Do(func() {})
	old := txIngestHighWaterMark
	txIngestHighWaterMark = 1000
	t.Cleanup(func() { txIngestHighWaterMark = old })

	pool := &fakeTxsPool{pending: 1, queued: 1} // 2 < 1000
	txAPI := NewTransactionAPI(&API{txspool: pool}, new(AddrLocker))

	_, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes{0xde, 0xad})
	if err == nil {
		t.Fatal("expected a decode error for invalid input")
	}
	if err == errAboveHighWater {
		t.Fatal("gate rejected below the mark; must fall through to decode instead")
	}
}

// TestBatchRawTransactionAboveHighWaterSkipsDecode mirrors the single-tx
// case for the batch endpoint: one bad entry among the inputs would
// normally be skipped and the good ones still submitted, but above the
// mark the WHOLE batch is rejected before any entry is even looked at.
func TestBatchRawTransactionAboveHighWaterSkipsDecode(t *testing.T) {
	txIngestHighWaterOnce.Do(func() {})
	old := txIngestHighWaterMark
	txIngestHighWaterMark = 5
	t.Cleanup(func() { txIngestHighWaterMark = old })

	pool := &fakeTxsPool{pending: 5, queued: 0} // 5 >= 5
	txAPI := NewTransactionAPI(&API{txspool: pool}, new(AddrLocker))

	hs, err := txAPI.BatchRawTransaction(context.Background(), []hexutil.Bytes{{0xde, 0xad}, {0xbe, 0xef}})
	if err != errAboveHighWater {
		t.Fatalf("BatchRawTransaction error = %v, want errAboveHighWater", err)
	}
	if hs != nil {
		t.Fatalf("hashes = %v, want nil (nothing was even attempted)", hs)
	}
}
