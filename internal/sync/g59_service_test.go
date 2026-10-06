package sync

import (
	"context"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p"
)

// newServiceTestChain returns a minimal chain stub sufficient for
// NewService's currentForkDigest() call (CurrentBlock + GenesisBlock).
func newServiceTestChain() *rangeChainStub {
	return newRangeChainStub(0)
}

// TestNewServiceConstructsAndRegistersHandlers exercises NewService end to
// end against a real (socket-less) libp2p host + GossipSub router: RPC
// stream handlers and the block/blob (and, with a tx pool, transaction)
// gossip subscriptions must all register without error.
func TestNewServiceConstructsAndRegistersHandlers(t *testing.T) {
	fp := newFakeP2PWithHost(t)
	chain := newServiceTestChain()

	svc, err := NewService(context.Background(), WithP2P(fp), WithChainService(chain))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.cancel()

	if svc.subHandler == nil {
		t.Fatal("subHandler not initialized")
	}
	// registerRPCHandlers should have installed the block-push and
	// block-by-hash raw stream handlers on the real host.
	found := false
	for _, proto := range fp.realHost.Mux().Protocols() {
		if string(proto) != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected at least one stream protocol registered on host")
	}
	// The block and blob-sidecar gossip topics should be joined.
	if len(fp.realPubSub.GetTopics()) == 0 {
		t.Fatal("expected at least one joined pubsub topic")
	}
}

// TestNewServiceWithTxPoolSubscribesTransactionTopic verifies the
// txGossipEnabled branch of registerSubscribers actually subscribes.
func TestNewServiceWithTxPoolSubscribesTransactionTopic(t *testing.T) {
	fp := newFakeP2PWithHost(t)
	chain := newServiceTestChain()

	svc, err := NewService(context.Background(), WithP2P(fp), WithChainService(chain), WithTxPool(&syncUFakeTxPool{}))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.cancel()

	digest, err := svc.currentForkDigest()
	if err != nil {
		t.Fatalf("currentForkDigest: %v", err)
	}
	txTopic := svc.addDigestToTopic(p2p.TransactionTopicFormat, digest) + fp.Encoding().ProtocolSuffix()
	if !svc.subHandler.topicExists(txTopic) {
		t.Fatalf("expected tx gossip topic %q to be subscribed", txTopic)
	}
}

// syncUFakeTxPool is a no-op TxPool implementation, just enough to flip
// txGossipEnabled via WithTxPool.
type syncUFakeTxPool struct{}

func (syncUFakeTxPool) AddRemotes(txs []*transaction.Transaction) []error { return nil }
func (syncUFakeTxPool) GetTx(hash types.Hash) *transaction.Transaction    { return nil }

// TestServiceRegisterHandlersStartStop exercises RegisterHandlers, Start and
// Stop on a fully constructed Service, including Status() and
// SetEarliestBlock/SetBlockImportNotifier, with a bounded wait for
// background goroutines to exit after Stop.
func TestServiceRegisterHandlersStartStop(t *testing.T) {
	fp := newFakeP2PWithHost(t)
	chain := newServiceTestChain()

	svc, err := NewService(context.Background(), WithP2P(fp), WithChainService(chain))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	svc.SetEarliestBlock(func() uint64 { return 0 })
	svc.SetBlockImportNotifier(nil)

	svc.RegisterHandlers()
	svc.Start()

	// Status should report in-sync: no peers, so HighestBlockNumber is 0.
	if err := svc.Status(); err != nil {
		t.Fatalf("Status() = %v, want nil", err)
	}

	done := make(chan struct{})
	go func() {
		if err := svc.Stop(); err != nil {
			t.Errorf("Stop() = %v", err)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() did not return within 5s — possible goroutine leak")
	}
}
