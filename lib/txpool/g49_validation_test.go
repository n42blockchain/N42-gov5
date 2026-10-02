package txpool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/types"
)

// TestValidateSerializedTxn covers the size-gate branches: a normal
// txn under the legacy/dynamic-fee cap, one over it, a blob txn under
// the (larger) blob cap, and one over it.
func TestValidateSerializedTxn(t *testing.T) {
	pool := &TxPool{}

	// Legacy txn (type byte 0x00 prefix would be ambiguous with RLP
	// list encoding; PeekTransactionType treats a leading byte in
	// [0xc0,0xfe] as LegacyTxType). Use a minimal valid-looking RLP
	// list prefix.
	legacySmall := []byte{0xc2, 0x01, 0x02}
	if err := pool.ValidateSerializedTxn(legacySmall); err != nil {
		t.Errorf("small legacy txn should validate: %v", err)
	}

	legacyTooBig := make([]byte, 128*1024+1)
	legacyTooBig[0] = 0xc2
	if err := pool.ValidateSerializedTxn(legacyTooBig); err != types.ErrRlpTooBig {
		t.Errorf("expected ErrRlpTooBig for oversized legacy txn, got %v", err)
	}

	// DynamicFeeTxType = 0x02 prefix byte.
	dynSmall := []byte{0x02, 0xc2, 0x01, 0x02}
	if err := pool.ValidateSerializedTxn(dynSmall); err != nil {
		t.Errorf("small dynamic-fee txn should validate: %v", err)
	}

	// BlobTxType = 0x03 prefix byte — cap is 800_000, well above the
	// 128KB cap, so a txn between those two sizes should PASS here
	// but would fail the legacy/dynamic cap.
	blobMid := make([]byte, 200*1024)
	blobMid[0] = 0x03
	if err := pool.ValidateSerializedTxn(blobMid); err != nil {
		t.Errorf("mid-size blob txn should validate under the larger blob cap: %v", err)
	}

	blobTooBig := make([]byte, 800_001)
	blobTooBig[0] = 0x03
	if err := pool.ValidateSerializedTxn(blobTooBig); err != types.ErrRlpTooBig {
		t.Errorf("expected ErrRlpTooBig for oversized blob txn, got %v", err)
	}
}

// TestPunishSpammer verifies punishSpammer drops roughly half of a
// sender's queued transactions (the highest-nonce half), via the
// production AddLocalTxs path.
func TestPunishSpammer(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	var txSlots types.TxSlots
	for i, nonce := range []uint64{0, 1, 2, 3, 4, 5} {
		s := &types.TxSlot{
			Tip:    *uint256.NewInt(300000),
			FeeCap: *uint256.NewInt(300000),
			Gas:    100000,
			Nonce:  nonce,
		}
		s.IDHash[0] = byte(i + 1)
		s.Rlp = []byte{0xaa, byte(i + 1)}
		txSlots.Append(s, addr[:], true)
	}
	_, err = pool.AddLocalTxs(ctx, txSlots, tx)
	require.NoError(t, err)

	senderID, ok := pool.senders.getID(addr)
	if !ok {
		t.Fatal("expected sender to be registered after AddLocalTxs")
	}

	before := pool.all.count(senderID)
	if before == 0 {
		t.Fatal("expected a non-zero count of txns for the sender before punishment")
	}

	pool.lock.Lock()
	pool.punishSpammer(senderID)
	pool.lock.Unlock()

	after := pool.all.count(senderID)
	if after >= before {
		t.Errorf("expected punishSpammer to drop txns: before=%d after=%d", before, after)
	}
	wantRemoved := before / 2
	gotRemoved := before - after
	if gotRemoved != wantRemoved {
		t.Errorf("expected to remove %d (before/2), removed %d (before=%d after=%d)", wantRemoved, gotRemoved, before, after)
	}
}
