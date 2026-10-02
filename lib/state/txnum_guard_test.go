package state

import (
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeTxNumPrefix(t *testing.T) {
	buf := make([]byte, 10)
	binary.BigEndian.PutUint64(buf, 42)

	n, err := decodeTxNumPrefix("ctx", buf)
	require.NoError(t, err)
	require.EqualValues(t, 42, n)

	_, err = decodeTxNumPrefix("ctx", buf[:4])
	require.Error(t, err)
}

func TestDecodeTxNumExact(t *testing.T) {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, 7)

	n, err := decodeTxNumExact("ctx", buf)
	require.NoError(t, err)
	require.EqualValues(t, 7, n)

	_, err = decodeTxNumExact("ctx", append(buf, 0x00))
	require.Error(t, err)

	_, err = decodeTxNumExact("ctx", buf[:7])
	require.Error(t, err)
}

func TestSplitAndTrimTxNumSuffix(t *testing.T) {
	key := make([]byte, 12)
	copy(key, []byte{0x01, 0x02, 0x03, 0x04})
	binary.BigEndian.PutUint64(key[4:], 99)

	prefix, txNum, err := splitTxNumSuffix("ctx", key)
	require.NoError(t, err)
	require.Equal(t, key[:4], prefix)
	require.EqualValues(t, 99, txNum)

	gotPrefix, err := trimTxNumSuffix("ctx", key)
	require.NoError(t, err)
	require.Equal(t, prefix, gotPrefix)

	_, _, err = splitTxNumSuffix("ctx", key[:4])
	require.Error(t, err)

	_, err = trimTxNumSuffix("ctx", key[:4])
	require.Error(t, err)
}
