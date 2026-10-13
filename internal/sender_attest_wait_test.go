package internal

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/store"
)

type delayedSenderSource struct {
	ready     atomic.Bool
	addrs     []types.Address
	recovered atomic.Int64
	served    atomic.Int64
}

func (s *delayedSenderSource) Senders(types.Hash, int) []types.Address {
	if s.ready.Load() {
		s.served.Add(1)
		return s.addrs
	}
	return nil
}
func (s *delayedSenderSource) Pending(types.Hash) bool              { return true }
func (s *delayedSenderSource) Recovered(types.Hash, *store.Senders) { s.recovered.Add(1) }

func TestSenderJobUsesLateAttestationBeforeFallback(t *testing.T) {
	prev := frameAttestWait
	frameAttestWait = 50 * time.Millisecond
	t.Cleanup(func() { frameAttestWait = prev })
	for _, stored := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-stored", true: "stored"}[stored], func(t *testing.T) {
			txs, want := buildSignedTxs(t, 32)
			src := &delayedSenderSource{addrs: want}
			j := &senderJob{signer: transaction.NewLondonSigner(senderRecoveryChainID), txs: txs, spans: []FrameSpan{{H: types.Hash{1}, Hi: len(txs)}}, src: src, start: time.Now()}
			if stored {
				j.st = store.New(1024)
				j.st.Put(types.Hash{1}, []byte{1})
			}
			delivered := make(chan struct{})
			go func() { time.Sleep(5 * time.Millisecond); src.ready.Store(true); close(delivered) }()
			j.join(1)
			usedAttestation := src.served.Load() > 0
			<-delivered
			// A delayed attestation must settle the frame without the local signature path.
			if !j.complete() {
				t.Fatal("frame job did not complete")
			}
			for i, tx := range txs {
				got, err := transaction.Sender(j.signer, tx)
				if err != nil || got != want[i] {
					t.Fatalf("sender %d mismatch", i)
				}
			}
			if got := src.recovered.Load(); got != 0 {
				t.Fatalf("premature local frame recoveries: %d", got)
			}
			if !usedAttestation {
				t.Fatal("sender job finished before attestation")
			}
		})
	}
}

func TestSenderJobAttestationWaitExpiresForWholeBlock(t *testing.T) {
	prev := frameAttestWait
	frameAttestWait = 10 * time.Millisecond
	t.Cleanup(func() { frameAttestWait = prev })
	txs, want := buildSignedTxs(t, 32)
	src := &delayedSenderSource{addrs: want}
	spans := make([]FrameSpan, len(txs))
	for i := range spans {
		spans[i] = FrameSpan{H: types.Hash{byte(i + 1)}, Lo: i, Hi: i + 1}
	}
	j := &senderJob{signer: transaction.NewLondonSigner(senderRecoveryChainID), txs: txs, spans: spans, src: src, start: time.Now()}
	j.join(2)
	if !j.complete() {
		t.Fatal("missing attestations did not fall back")
	}
	if took := time.Since(j.start); took > 150*time.Millisecond {
		t.Fatalf("wait was renewed per frame: %v", took)
	}
	for i, tx := range txs {
		got, err := transaction.Sender(j.signer, tx)
		if err != nil || got != want[i] {
			t.Fatalf("fallback sender %d mismatch", i)
		}
	}
}

func TestSenderJobAttestationWaitCanBeDisabled(t *testing.T) {
	prev := frameAttestWait
	frameAttestWait = 0
	t.Cleanup(func() { frameAttestWait = prev })
	src := &delayedSenderSource{}
	j := &senderJob{src: src, start: time.Now()}
	if got := j.fromSource(types.Hash{1}, 1, true); got != nil {
		t.Fatal("missing source answered")
	}
}
