package txpool

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/crypto"
	"github.com/n42blockchain/N42/lib/kv/kvcache"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

// g64SignedAuthorization builds an EIP-7702 authorization tuple
// (chain_id, address, nonce) exactly as parseAuthorizations lays it out in
// AuthRaw (rlp.EncodeToBytes of each field, concatenated — no outer list
// wrapper, matching payload[rawStart:rawEnd] in txn.go), signed the same way
// RecoverSignerFromRLP (auth.go) verifies it: keccak256(0x05 || authRaw).
// This gives discardLocked's SetCodeTxType cleanup branch a signature that
// actually recovers, rather than erroring out on the (skip) path.
func g64SignedAuthorization(t *testing.T) (types.Signature, []byte) {
	t.Helper()
	priv, err := crypto.GenerateKey()
	require.NoError(t, err)

	chainIDRlp, err := rlp.EncodeToBytes(uint64(1))
	require.NoError(t, err)
	addr := [20]byte{0x99}
	addrRlp, err := rlp.EncodeToBytes(addr[:])
	require.NoError(t, err)
	nonceRlp, err := rlp.EncodeToBytes(uint64(7))
	require.NoError(t, err)

	authRaw := append(append(append([]byte{}, chainIDRlp...), addrRlp...), nonceRlp...)
	hash := crypto.Keccak256Hash(append([]byte{0x05}, authRaw...))
	sig, err := crypto.Sign(hash[:], priv)
	require.NoError(t, err)
	require.Len(t, sig, 65)

	var out types.Signature
	out.ChainID.SetUint64(1)
	out.V.SetUint64(uint64(sig[64]))
	out.R.SetBytes(sig[0:32])
	out.S.SetBytes(sig[32:64])
	return out, authRaw
}

// TestG64DiscardLocked_SetCodeCleansAuthorization covers discardLocked's
// SetCodeTxType branch (lifecycle.go:156): discarding a SetCode txn must
// recover each authorization's signer and remove it from p.auths, freeing
// the authority for a future txn (addLocked's own duplicate-authority check
// reads this same map).
func TestG64DiscardLocked_SetCodeCleansAuthorization(t *testing.T) {
	sig, authRaw := g64SignedAuthorization(t)
	signer, err := RecoverSignerFromRLP(authRaw, uint8(sig.V.Uint64()), sig.R, sig.S)
	require.NoError(t, err)
	require.NotNil(t, signer)

	txn := &types.TxSlot{Type: types.SetCodeTxType}
	txn.Authorizations = append(txn.Authorizations, sig)
	txn.AuthRaw = append(txn.AuthRaw, authRaw)

	pool := g64BarePool(t)
	mt := newMetaTx(txn, false, 0)
	// Simulate addLocked having reserved this authority for mt (the branch
	// under test only cleans up what addLocked put there).
	pool.auths[*signer] = mt
	_, reserved := pool.auths[*signer]
	require.True(t, reserved)

	pool.discardLocked(mt, txpoolcfg.ReplacedByHigherTip)

	_, stillReserved := pool.auths[*signer]
	require.False(t, stillReserved, "discardLocked must free the authority reserved by the discarded SetCode txn")
}

// g64BarePool is a minimal pool (no funded senders, no OnNewBlock) for tests
// that only need p.auths/p.byHash/p.all machinery via discardLocked/addLocked
// directly, without going through the full OnNewBlock bring-up.
func g64BarePool(t *testing.T) *TxPool {
	t.Helper()
	ch := make(chan types.Announcements, 100)
	coreDB := memdb.NewTestDB(t)
	sendersCache := kvcache.New(kvcache.DefaultCoherentConfig)
	pool, err := New(ch, coreDB, txpoolcfg.DefaultConfig, sendersCache, *uint256.NewInt(1), nil, nil, nil, nil, nil, nil, nil, nil, log.New())
	require.NoError(t, err)
	return pool
}
