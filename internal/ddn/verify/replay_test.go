package verify

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ddn/receipt"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func TestConcurrentReceiptReplay(t *testing.T) {
	key, _ := crypto.GenerateKey()
	s, _ := receipt.NewSigner(key)
	defer s.Close()
	now := uint64(time.Now().UnixMilli())
	req := d.DecisionRequest{ChainID: 94, Version: 1, Task: "node.anomaly", SchemaID: "health-v1", InputHash: chain.Hash{1}, Quorum: 1, MaxLatencyMs: 100, MaxCost: "0", Deadline: now + 60000, Nonce: 1, Requester: "did:n42:test", PrivacyMode: "public"}
	id, _ := req.CanonicalHash()
	r, _ := s.Sign(d.DecisionReceipt{ChainID: 94, Version: 1, RequestID: id, ProviderDID: s.DID(), InputHash: req.InputHash, ModelHash: chain.Hash{2}, Nonce: 1, StartedAt: now, CompletedAt: now, Expiry: now + 1000, Result: d.DecisionResult{Label: "NORMAL"}})
	v, _ := New(1)
	var wg sync.WaitGroup
	var accepted atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v.Consume(r, req, s.Address(), now) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("replay cache failed atomic consume")
	}
}
