package state

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

// g65CommittedDomain builds a DomainCommitted over a fresh temp-dir MDBX
// domain, mirroring testDbAndDomain's construction but wiring the
// commitment layer on top via NewCommittedDomain.
func g65CommittedDomain(t *testing.T) *DomainCommitted {
	t.Helper()
	logger := log.New()
	_, db, d := testDbAndDomain(t, logger)

	ctx := context.Background()
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)
	d.SetTx(tx)

	return NewCommittedDomain(d, CommitmentModeDirect, commitment.VariantHexPatriciaTrie, logger)
}

func TestNewCommittedDomainConstructs(t *testing.T) {
	dc := g65CommittedDomain(t)
	if dc.Domain == nil {
		t.Fatalf("expected the embedded Domain to be set")
	}
	if dc.mode != CommitmentModeDirect {
		t.Fatalf("mode = %v, want CommitmentModeDirect", dc.mode)
	}
	if dc.patriciaTrie == nil {
		t.Fatalf("expected a patricia trie to be initialized")
	}
}

func TestDomainCommittedTouchPlainKeyDisabledModeIsNoOp(t *testing.T) {
	dc := g65CommittedDomain(t)
	dc.SetCommitmentMode(CommitmentModeDisabled)

	called := false
	dc.TouchPlainKey([]byte("plainkey1234567890ab"), []byte("val"), func(c *CommitmentItem, val []byte) {
		called = true
	})
	if called {
		t.Fatalf("fn must not run when commitment mode is disabled")
	}
	plainKeys, _, _ := dc.TouchedKeyList()
	if len(plainKeys) != 0 {
		t.Fatalf("expected no touched keys when commitment mode is disabled")
	}
}

func TestDomainCommittedTouchPlainKeyAccountAndCode(t *testing.T) {
	dc := g65CommittedDomain(t)
	dc.SetCommitmentMode(CommitmentModeUpdate)

	key := make([]byte, length.Addr)
	copy(key, []byte("acct-address-20byte!"))

	// Touch as account first.
	dc.TouchPlainKey(key, []byte{1, 2, 3}, dc.TouchPlainKeyAccount)
	// Touch the same key as code; TouchPlainKeyCode should see the prior
	// account entry and merge its balance/nonce flags in.
	dc.TouchPlainKey(key, []byte("some-code-bytes"), dc.TouchPlainKeyCode)

	plainKeys, hashedKeys, updates := dc.TouchedKeyList()
	if len(plainKeys) != 1 {
		t.Fatalf("expected exactly one merged commitment item, got %d", len(plainKeys))
	}
	if len(hashedKeys[0]) == 0 {
		t.Fatalf("expected a non-empty hashed key")
	}
	if updates[0].Flags&commitment.CodeUpdate == 0 {
		t.Fatalf("expected the merged update to carry the CodeUpdate flag")
	}
}

func TestDomainCommittedTouchPlainKeyStorage(t *testing.T) {
	dc := g65CommittedDomain(t)
	dc.SetCommitmentMode(CommitmentModeUpdate)

	key := append(make([]byte, length.Addr), []byte("storage-slot-bytes32pad")...)

	dc.TouchPlainKey(key, []byte("storage-value"), dc.TouchPlainKeyStorage)
	_, _, updates := dc.TouchedKeyList()
	if len(updates) != 1 {
		t.Fatalf("expected one touched key, got %d", len(updates))
	}
	if updates[0].Flags&commitment.StorageUpdate == 0 {
		t.Fatalf("expected the StorageUpdate flag to be set")
	}

	// A delete (empty value) must flip the flag to DeleteUpdate.
	dc2 := g65CommittedDomain(t)
	dc2.SetCommitmentMode(CommitmentModeUpdate)
	dc2.TouchPlainKey(key, nil, dc2.TouchPlainKeyStorage)
	_, _, updates2 := dc2.TouchedKeyList()
	if updates2[0].Flags != commitment.DeleteUpdate {
		t.Fatalf("expected DeleteUpdate for an empty storage value, got %v", updates2[0].Flags)
	}
}

func TestHashAndNibblizeKeyProducesNibbles(t *testing.T) {
	dc := g65CommittedDomain(t)

	key := append(make([]byte, length.Addr), []byte("some-storage-key-bytes!!")...)
	nibbles := dc.hashAndNibblizeKey(key)

	// Two hashed halves (address + storage) each expand to 2*length.Hash nibbles.
	if len(nibbles) != 4*length.Hash {
		t.Fatalf("len(nibbles) = %d, want %d", len(nibbles), 4*length.Hash)
	}
	for _, n := range nibbles {
		if n > 0xf {
			t.Fatalf("nibble %#x exceeds 4 bits", n)
		}
	}

	accountOnly := make([]byte, length.Addr)
	nibblesAccount := dc.hashAndNibblizeKey(accountOnly)
	if len(nibblesAccount) != 2*length.Hash {
		t.Fatalf("account-only key: len(nibbles) = %d, want %d", len(nibblesAccount), 2*length.Hash)
	}
}

func TestComputeCommitmentReturnsNotYetAdaptedError(t *testing.T) {
	dc := g65CommittedDomain(t)

	root, updates, err := dc.ComputeCommitment(false)
	if err == nil {
		t.Fatalf("expected ComputeCommitment to report it is not yet adapted")
	}
	if root != nil || updates != nil {
		t.Fatalf("expected nil root/updates alongside the error")
	}
}

func TestSeekCommitmentWithNoStoredStateReturnsZero(t *testing.T) {
	dc := g65CommittedDomain(t)

	blockNum, txNum, err := dc.SeekCommitment(16, 16)
	require.NoError(t, err)
	if blockNum != 0 || txNum != 0 {
		t.Fatalf("blockNum=%d txNum=%d, want 0,0 with no stored commitment state", blockNum, txNum)
	}
}

func TestSeekCommitmentRejectsNonHexPatriciaVariant(t *testing.T) {
	logger := log.New()
	_, db, d := testDbAndDomain(t, logger)
	ctx := context.Background()
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)
	d.SetTx(tx)

	dc := NewCommittedDomain(d, CommitmentModeDirect, commitment.VariantConcurrentHexPatricia, logger)
	_, _, err = dc.SeekCommitment(16, 16)
	if err == nil {
		t.Fatalf("expected an error for a non hex-patricia trie variant")
	}
}

func TestCommitmentModeStringAndParse(t *testing.T) {
	if got := CommitmentModeDisabled.String(); got != "disabled" {
		t.Fatalf("String() = %q, want %q", got, "disabled")
	}
	if got := CommitmentModeDirect.String(); got != "direct" {
		t.Fatalf("String() = %q, want %q", got, "direct")
	}
	if got := CommitmentModeUpdate.String(); got != "update" {
		t.Fatalf("String() = %q, want %q", got, "update")
	}
	if got := CommitmentMode(99).String(); got != "unknown" {
		t.Fatalf("String() = %q, want %q", got, "unknown")
	}

	if ParseCommitmentMode("off") != CommitmentModeDisabled {
		t.Fatalf("ParseCommitmentMode(off) != disabled")
	}
	if ParseCommitmentMode("update") != CommitmentModeUpdate {
		t.Fatalf("ParseCommitmentMode(update) != update")
	}
	if ParseCommitmentMode("anything-else") != CommitmentModeDirect {
		t.Fatalf("ParseCommitmentMode(other) != direct")
	}
}
