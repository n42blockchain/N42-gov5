package transaction

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEIP155SignerChainIDAndEqual(t *testing.T) {
	r := require.New(t)
	s1 := NewEIP155Signer(big.NewInt(1))
	s2 := NewEIP155Signer(big.NewInt(1))
	s3 := NewEIP155Signer(big.NewInt(2))

	r.Equal(big.NewInt(1), s1.ChainID())
	r.True(s1.Equal(s2))
	r.False(s1.Equal(s3))
	r.False(s1.Equal(FrontierSigner{}))
}

func TestEIP2930SignerChainIDAndEqual(t *testing.T) {
	r := require.New(t)
	s1 := NewEIP2930Signer(big.NewInt(5))
	s2 := NewEIP2930Signer(big.NewInt(5))
	s3 := NewEIP2930Signer(big.NewInt(6))

	r.Equal(big.NewInt(5), s1.ChainID())
	r.True(s1.Equal(s2))
	r.False(s1.Equal(s3))
	r.False(s1.Equal(NewEIP155Signer(big.NewInt(5))))
}

func TestHomesteadSignerChainIDAndEqual(t *testing.T) {
	r := require.New(t)
	s := HomesteadSigner{}
	r.Nil(s.ChainID())
	r.True(s.Equal(HomesteadSigner{}))
	r.False(s.Equal(FrontierSigner{}))
}

func TestFrontierSignerChainIDAndEqual(t *testing.T) {
	r := require.New(t)
	s := FrontierSigner{}
	r.Nil(s.ChainID())
	r.True(s.Equal(FrontierSigner{}))
	r.False(s.Equal(HomesteadSigner{}))
}

func TestHomesteadSignerSenderRejectsNonLegacyType(t *testing.T) {
	r := require.New(t)
	tx := newTestSetCodeTx()
	wrapped := NewTx(tx)
	_, err := (HomesteadSigner{}).Sender(wrapped)
	r.ErrorIs(err, ErrTxTypeNotSupported)
}

func TestFrontierSignerSenderRejectsNonLegacyType(t *testing.T) {
	r := require.New(t)
	tx := newTestSetCodeTx()
	wrapped := NewTx(tx)
	_, err := (FrontierSigner{}).Sender(wrapped)
	r.ErrorIs(err, ErrTxTypeNotSupported)
}
