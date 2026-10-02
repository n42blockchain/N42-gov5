package state

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeAccountBytes(t *testing.T) {
	hash := make([]byte, 32)
	hash[0] = 0xAB

	enc := EncodeAccountBytes(7, uint256.NewInt(1234), hash, 3)
	require.NotEmpty(t, enc)

	nonce, balance, gotHash := DecodeAccountBytes(enc)
	require.EqualValues(t, 7, nonce)
	require.EqualValues(t, uint256.NewInt(1234), balance)
	require.Equal(t, hash, gotHash)
}

func TestEncodeDecodeAccountBytesZeroValues(t *testing.T) {
	enc := EncodeAccountBytes(0, uint256.NewInt(0), nil, 0)
	require.NotEmpty(t, enc)

	nonce, balance, hash := DecodeAccountBytes(enc)
	require.EqualValues(t, 0, nonce)
	require.True(t, balance.IsZero())
	require.Nil(t, hash)
}

func TestDecodeAccountBytesEmpty(t *testing.T) {
	nonce, balance, hash := DecodeAccountBytes(nil)
	require.EqualValues(t, 0, nonce)
	require.True(t, balance.IsZero())
	require.Nil(t, hash)
}

func TestSelectedStaticFilesAndMergedFilesCloseNilSafe(t *testing.T) {
	var sf SelectedStaticFiles
	sf.Close() // all-nil slices: must not panic

	var mf MergedFiles
	mf.Close() // all-nil items: must not panic
}
