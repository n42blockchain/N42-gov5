package txpool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/crypto"
	gointerfacetypes "github.com/n42blockchain/N42/lib/gointerfaces/types"
	"github.com/n42blockchain/N42/lib/types"
)

// TestRecoverSignerFromRLP_Valid signs the EIP-7702-style authorization hash
// (0x05 || rlp) with a fresh key and checks RecoverSignerFromRLP recovers
// the matching address.
func TestRecoverSignerFromRLP_Valid(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	wantAddr := crypto.PubkeyToAddress(key.PublicKey)

	rlp := []byte{0xc0, 0x01, 0x02, 0x03}
	hashData := append([]byte{0x05}, rlp...)
	hash := crypto.Keccak256Hash(hashData)

	sig, err := crypto.Sign(hash.Bytes(), key)
	require.NoError(t, err)

	var r, s uint256.Int
	r.SetBytes(sig[:32])
	s.SetBytes(sig[32:64])
	yParity := sig[64]

	got, err := RecoverSignerFromRLP(rlp, yParity, r, s)
	require.NoError(t, err)
	require.NotNil(t, got)
	if got.Hex() != wantAddr.Hex() {
		t.Fatalf("recovered address = %s, want %s", got.Hex(), wantAddr.Hex())
	}
}

// TestRecoverSignerFromRLP_InvalidYParity covers the yParity-out-of-range
// rejection branch.
func TestRecoverSignerFromRLP_InvalidYParity(t *testing.T) {
	_, err := RecoverSignerFromRLP([]byte{0x01}, 2, *uint256.NewInt(1), *uint256.NewInt(1))
	if err == nil {
		t.Fatal("expected an error for yParity=2")
	}
}

// TestRecoverSignerFromRLP_InvalidSignature covers the signature-validity
// rejection branch (r, s both zero is never a valid secp256k1 signature).
func TestRecoverSignerFromRLP_InvalidSignature(t *testing.T) {
	_, err := RecoverSignerFromRLP([]byte{0x01}, 0, *uint256.NewInt(0), *uint256.NewInt(0))
	if err == nil {
		t.Fatal("expected an error for an all-zero signature")
	}
}

// TestPool_SimpleAccessors exercises IsLocal, AddNewGoodPeer, Started,
// logStats, and deprecatedForEach against a real funded pool.
func TestPool_SimpleAccessors(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()

	// Started() reflects Start(); the pool from g49NewFundedPool has only
	// had OnNewBlock applied directly, not Start(), so this covers the
	// false branch deterministically.
	if pool.Started() {
		t.Fatal("expected Started()==false before Start() is called")
	}

	// logStats() must no-op (not panic) while not started.
	pool.logStats()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	s := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  0,
	}
	s.IDHash[0] = 0x55
	s.Rlp = []byte{0xde, 0xad, 0x55}
	var txSlots types.TxSlots
	txSlots.Append(s, addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, txSlots, tx)
	require.NoError(t, err)
	require.Len(t, reasons, 1)

	if !pool.IsLocal(s.IDHash[:]) {
		t.Error("expected the just-added local tx to be recognized by IsLocal")
	}
	randomHash := [32]byte{0xff}
	if pool.IsLocal(randomHash[:]) {
		t.Error("expected an unknown hash to not be recognized by IsLocal")
	}

	var peerID gointerfacetypes.H512
	pool.AddNewGoodPeer(types.PeerID(&peerID))

	// deprecatedForEach must visit at least the one transaction we added.
	visited := 0
	pool.deprecatedForEach(ctx, func(rlp []byte, sender common.Address, st SubPoolType) {
		visited++
	}, tx)
	if visited == 0 {
		t.Error("expected deprecatedForEach to visit at least one transaction")
	}
}
