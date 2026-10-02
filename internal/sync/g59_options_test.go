package sync

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestOptionsApplyToConfig exercises every functional Option against a bare
// Service, asserting each lands in s.cfg as expected.
func TestOptionsApplyToConfig(t *testing.T) {
	fp := newFakeP2P(t)
	chain := newServiceTestChain()
	hash := types.BytesToHash([]byte{0xAB})
	var notified types.Hash
	notifier := syncUFakeNotifier{onImported: func(h, _ types.Hash) { notified = h }}
	pool := &syncUFakeTxPool{}

	s := &Service{cfg: &config{}}
	opts := []Option{
		WithP2P(fp),
		WithChainService(chain),
		WithInitialSync(syncUFakeChecker{}),
		WithOverrideGenesisHash(hash),
		WithEarliestBlock(func() uint64 { return 7 }),
		WithBlockImportNotifier(notifier),
		WithTxPool(pool),
	}
	for _, opt := range opts {
		if err := opt(s); err != nil {
			t.Fatalf("option returned error: %v", err)
		}
	}

	if s.cfg.p2p != fp {
		t.Error("WithP2P did not set cfg.p2p")
	}
	if s.cfg.chain != chain {
		t.Error("WithChainService did not set cfg.chain")
	}
	if s.cfg.initialSync == nil {
		t.Error("WithInitialSync did not set cfg.initialSync")
	}
	if s.cfg.overrideGenesisHash == nil || *s.cfg.overrideGenesisHash != hash {
		t.Error("WithOverrideGenesisHash did not set cfg.overrideGenesisHash")
	}
	if s.cfg.earliestBlock == nil || s.cfg.earliestBlock() != 7 {
		t.Error("WithEarliestBlock did not set cfg.earliestBlock")
	}
	if s.cfg.blockImportNotifier == nil {
		t.Error("WithBlockImportNotifier did not set cfg.blockImportNotifier")
	}
	s.cfg.blockImportNotifier.NotifyBlockImported(hash, types.Hash{})
	if notified != hash {
		t.Error("blockImportNotifier not wired through")
	}
	if s.cfg.txPool != pool {
		t.Error("WithTxPool did not set cfg.txPool")
	}
	if !s.cfg.txGossipEnabled {
		t.Error("WithTxPool(non-nil) did not enable tx gossip")
	}
}

// TestWithTxPoolNilDisablesGossip covers the false branch of txGossipEnabled.
func TestWithTxPoolNilDisablesGossip(t *testing.T) {
	s := &Service{cfg: &config{}}
	if err := WithTxPool(nil)(s); err != nil {
		t.Fatalf("WithTxPool(nil): %v", err)
	}
	if s.cfg.txGossipEnabled {
		t.Error("WithTxPool(nil) should leave tx gossip disabled")
	}
}

type syncUFakeChecker struct{}

func (syncUFakeChecker) Syncing() bool { return false }
func (syncUFakeChecker) Synced() bool  { return true }
func (syncUFakeChecker) Status() error { return nil }
func (syncUFakeChecker) Resync() error { return nil }

type syncUFakeNotifier struct {
	onImported func(hash, txHash types.Hash)
}

func (n syncUFakeNotifier) NotifyBlockImported(hash, txHash types.Hash) {
	if n.onImported != nil {
		n.onImported(hash, txHash)
	}
}
func (syncUFakeNotifier) NotifyBlockChecked(hash, parent types.Hash)   {}
func (syncUFakeNotifier) NotifyBlockRejected(hash types.Hash)          {}
func (syncUFakeNotifier) NotifyBlockHeaderKnown(hash, parent types.Hash, number uint64) {}
