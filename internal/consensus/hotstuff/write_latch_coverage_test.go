package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// TestGetOrCreateWriteLatch_ReuseAndReset covers: first-arrival creation,
// reuse on a second lookup for the same hash, and the defensive map reset
// once writeLatchMaxEntries is exceeded.
func TestGetOrCreateWriteLatch_ReuseAndReset(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	h1 := types.Hash{0x01}
	l1 := engine.getOrCreateWriteLatch(h1)
	l1Again := engine.getOrCreateWriteLatch(h1)
	if l1 != l1Again {
		t.Fatalf("expected the same latch instance on reuse")
	}

	// Fill past the cap to trigger the reset branch.
	for i := 0; i < writeLatchMaxEntries+1; i++ {
		var h types.Hash
		h[0] = byte(i % 256)
		h[1] = byte(i / 256)
		engine.getOrCreateWriteLatch(h)
	}
	engine.writeLatchMu.Lock()
	n := len(engine.writeLatches)
	engine.writeLatchMu.Unlock()
	if n > writeLatchMaxEntries {
		t.Fatalf("expected the latch map to stay bounded, got %d entries", n)
	}
}

// TestWaitForCommitVoteJournal_OffByDefault covers the default no-op branch.
func TestWaitForCommitVoteJournal_OffByDefault(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	if leaderWriteAfterJournalEnabled {
		t.Skip("N42_LEADER_WRITE_AFTER_JOURNAL is set in this environment; off-path not reachable")
	}
	waited, why := engine.WaitForCommitVoteJournal(types.Hash{0x02}, 10*time.Millisecond)
	if why != "off" {
		t.Fatalf("expected why=off, got %q", why)
	}
	if waited != 0 {
		t.Fatalf("expected zero wait in the off path, got %v", waited)
	}
}

// TestWaitForCommitVoteJournal_FireBeforeWait and FireAfterWait exercise the
// enabled path end to end by toggling the package-level switch directly
// (white-box, same package) and restoring it afterward.
func TestWaitForCommitVoteJournal_FireBeforeWait(t *testing.T) {
	prev := leaderWriteAfterJournalEnabled
	leaderWriteAfterJournalEnabled = true
	defer func() { leaderWriteAfterJournalEnabled = prev }()

	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	hash := types.Hash{0x03}
	engine.fireWriteLatch(hash, "journal")
	waited, why := engine.WaitForCommitVoteJournal(hash, 200*time.Millisecond)
	if why != "journal" {
		t.Fatalf("expected why=journal, got %q", why)
	}
	_ = waited

	// Consumed: the map entry was deleted so a second wait creates a fresh,
	// unfired latch (bounded by the short timeout below -> "timeout").
	waited2, why2 := engine.WaitForCommitVoteJournal(hash, 20*time.Millisecond)
	if why2 != "timeout" {
		t.Fatalf("expected why=timeout on the fresh latch, got %q", why2)
	}
	if waited2 < 15*time.Millisecond {
		t.Fatalf("expected the wait to approach the timeout, got %v", waited2)
	}
}

func TestWaitForCommitVoteJournal_WaitBeforeFire(t *testing.T) {
	prev := leaderWriteAfterJournalEnabled
	leaderWriteAfterJournalEnabled = true
	defer func() { leaderWriteAfterJournalEnabled = prev }()

	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	hash := types.Hash{0x04}
	resultCh := make(chan string, 1)
	go func() {
		_, why := engine.WaitForCommitVoteJournal(hash, 2*time.Second)
		resultCh <- why
	}()

	// Give the waiter a brief bounded head start, then fire.
	time.Sleep(20 * time.Millisecond)
	engine.fireWriteLatch(hash, "abandoned")

	select {
	case why := <-resultCh:
		if why != "abandoned" {
			t.Fatalf("expected why=abandoned, got %q", why)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("WaitForCommitVoteJournal did not return after Fire")
	}
}
