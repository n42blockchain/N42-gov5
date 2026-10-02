package transaction

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func newTestSetCodeTx() *SetCodeTx {
	to := types.Address{4}
	return &SetCodeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     3,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(0),
		V:         uint256.NewInt(0),
		R:         uint256.NewInt(1),
		S:         uint256.NewInt(1),
	}
}

func TestSetCodeTxFromAndSignAreCacheAndNil(t *testing.T) {
	r := require.New(t)
	tx := newTestSetCodeTx()
	r.Nil(tx.from())
	r.Nil(tx.sign())

	addr := types.Address{7}
	tx.fromCache = &addr
	r.Equal(&addr, tx.from())
}

func TestSetCodeTxSigningHash(t *testing.T) {
	r := require.New(t)
	tx := newTestSetCodeTx()
	h1 := tx.signingHash(big.NewInt(1))
	h2 := tx.signingHash(big.NewInt(1))
	r.Equal(h1, h2)

	h3 := tx.signingHash(big.NewInt(2))
	r.NotEqual(h1, h3, "signing hash must depend on chain ID")
}

func TestSetCodeTxEncodeRLP(t *testing.T) {
	r := require.New(t)
	tx := newTestSetCodeTx()
	enc, err := tx.EncodeRLP()
	r.NoError(err)
	r.NotEmpty(enc)
	r.Equal(byte(SetCodeTxType), enc[0])
}

func TestDelegationPrefixRoundTrip(t *testing.T) {
	r := require.New(t)
	prefix := DelegationPrefix()
	r.Equal([]byte{0xef, 0x01, 0x00}, prefix)
	// Returned slice is a copy: mutating it must not affect future calls.
	prefix[0] = 0x00
	r.Equal([]byte{0xef, 0x01, 0x00}, DelegationPrefix())
}

func TestParseDelegationAndAddressToDelegation(t *testing.T) {
	r := require.New(t)
	addr := types.Address{0x11, 0x22, 0x33}
	code := AddressToDelegation(addr)
	r.Len(code, 23)

	got, ok := ParseDelegation(code)
	r.True(ok)
	r.Equal(addr, got)

	// Wrong length.
	_, ok = ParseDelegation([]byte{0xef, 0x01, 0x00})
	r.False(ok)

	// Wrong prefix.
	bad := make([]byte, 23)
	copy(bad, []byte{0x00, 0x01, 0x00})
	_, ok = ParseDelegation(bad)
	r.False(ok)
}
