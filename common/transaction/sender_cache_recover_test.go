package transaction

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func TestRecoverSenderFromSigAndCache(t *testing.T) {
	r := require.New(t)
	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe512961708279c7b07b8d2f2b1dd1c7")
	r.NoError(err)
	expected := crypto.PubkeyToAddress(key.PublicKey)

	signer := NewEIP155Signer(big.NewInt(1))
	tx := NewTx(&LegacyTx{
		Nonce:    0,
		GasPrice: uint256.NewInt(1),
		Gas:      21000,
	})
	signed, err := SignTx(tx, signer, key)
	r.NoError(err)

	// Not cached yet.
	_, ok := CachedSender(signer, signed)
	r.False(ok)

	addr, err := RecoverSenderFromSig(signer, signed)
	r.NoError(err)
	r.Equal(expected, addr)

	// Now the cache has it.
	cached, ok := CachedSender(signer, signed)
	r.True(ok)
	r.Equal(expected, cached)

	// A second RecoverSenderFromSig call hits the cache path and returns the
	// same address.
	addr2, err := RecoverSenderFromSig(signer, signed)
	r.NoError(err)
	r.Equal(expected, addr2)
}

func TestCachedSenderNilArgs(t *testing.T) {
	r := require.New(t)
	_, ok := CachedSender(nil, nil)
	r.False(ok)

	signer := NewEIP155Signer(big.NewInt(1))
	_, ok = CachedSender(signer, nil)
	r.False(ok)
}

func TestTransactionCacheSender(t *testing.T) {
	r := require.New(t)
	signer := NewEIP155Signer(big.NewInt(1))
	tx := NewTx(&LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(2),
		Gas:      21000,
	})
	addr := types.Address{0x42}

	// CacheSender with a nil signer must be a no-op, not a panic.
	tx.CacheSender(nil, addr)
	_, ok := CachedSender(signer, tx)
	r.False(ok)

	tx.CacheSender(signer, addr)
	cached, ok := CachedSender(signer, tx)
	r.True(ok)
	r.Equal(addr, cached)

	// Sender() must now return the cached address without ECDSA recovery
	// (the tx has no valid signature, so a real recovery would fail).
	got, err := Sender(signer, tx)
	r.NoError(err)
	r.Equal(addr, got)
}
