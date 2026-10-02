package transaction

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func TestEthTransactionsLenAndEncodeIndex(t *testing.T) {
	r := require.New(t)
	to := types.Address{9}
	tx1 := NewTx(&LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(10),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(20),
		Data:     []byte{0x01},
		V:        uint256.NewInt(37),
		R:        uint256.NewInt(2),
		S:        uint256.NewInt(3),
	})
	tx2 := NewTx(&LegacyTx{
		Nonce:    2,
		GasPrice: uint256.NewInt(11),
		Gas:      21001,
		To:       &to,
		Value:    uint256.NewInt(21),
		V:        uint256.NewInt(38),
		R:        uint256.NewInt(4),
		S:        uint256.NewInt(5),
	})

	list := EthTransactions{tx1, tx2}
	r.Equal(2, list.Len())

	var buf0, buf1 bytes.Buffer
	list.EncodeIndex(0, &buf0)
	list.EncodeIndex(1, &buf1)
	r.NotEmpty(buf0.Bytes())
	r.NotEmpty(buf1.Bytes())
	r.NotEqual(buf0.Bytes(), buf1.Bytes())

	expected0, err := tx1.EthEncoded()
	r.NoError(err)
	r.Equal(expected0, buf0.Bytes())
}

func TestEthTransactionsEmptyList(t *testing.T) {
	require.Equal(t, 0, EthTransactions{}.Len())
}
