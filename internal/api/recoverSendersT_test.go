package api

// recoverSendersT_test.go covers EngineStateAdapter.recoverSenders: parallel
// ecrecover over a tx batch, skipping entries that already carry a cached
// From and leaving unrecoverable entries uncached rather than erroring.

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func TestEngineStateAdapterRecoverSenders(t *testing.T) {
	chainID := big.NewInt(1337)
	signer := transaction.LatestSignerForChainID(chainID)

	key1, err := crypto.GenerateKey()
	require.NoError(t, err)
	want1 := crypto.PubkeyToAddress(key1.PublicKey)

	key2, err := crypto.GenerateKey()
	require.NoError(t, err)
	want2 := crypto.PubkeyToAddress(key2.PublicKey)

	to := types.Address{0xbe, 0xef}

	tx1, err := transaction.SignNewTx(key1, signer, &transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(1),
	})
	require.NoError(t, err)

	tx2, err := transaction.SignNewTx(key2, signer, &transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(2),
	})
	require.NoError(t, err)

	// tx3 already has a cached From (e.g. seeded upstream): recoverSenders
	// must skip it rather than redo the recovery.
	tx3, err := transaction.SignNewTx(key1, signer, &transaction.LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(1_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(3),
	})
	require.NoError(t, err)
	preset := types.Address{0x01, 0x02}
	tx3.SetFrom(preset)

	a := NewEngineStateAdapter(nil, nil, nil, nil)
	txns := []*transaction.Transaction{tx1, tx2, tx3}
	a.recoverSenders(txns, signer)

	require.NotNil(t, tx1.From())
	require.Equal(t, want1, *tx1.From())
	require.NotNil(t, tx2.From())
	require.Equal(t, want2, *tx2.From())
	// Untouched: still the preset value, not re-recovered to want1.
	require.NotNil(t, tx3.From())
	require.Equal(t, preset, *tx3.From())
}

func TestEngineStateAdapterRecoverSendersEmptyAndSingle(t *testing.T) {
	a := NewEngineStateAdapter(nil, nil, nil, nil)
	signer := transaction.LatestSignerForChainID(big.NewInt(1337))

	// Empty slice: workers clamps to 0, nothing to wait on.
	require.NotPanics(t, func() { a.recoverSenders(nil, signer) })

	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	to := types.Address{0xaa}
	tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(5),
	})
	require.NoError(t, err)

	a.recoverSenders([]*transaction.Transaction{tx}, signer)
	require.NotNil(t, tx.From())
	require.Equal(t, crypto.PubkeyToAddress(key.PublicKey), *tx.From())
}
