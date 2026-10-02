package transaction

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func newLegacyForAccessors() *Transaction {
	to := types.Address{3}
	return NewTx(&LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(7),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(100),
		V:        uint256.NewInt(37),
		R:        uint256.NewInt(2),
		S:        uint256.NewInt(3),
	})
}

func TestTransactionsListLenAndEncodeIndex(t *testing.T) {
	r := require.New(t)
	tx1 := newLegacyForAccessors()
	tx2 := newLegacyForAccessors()
	tx2.SetNonce(2)

	list := Transactions{tx1, tx2}
	r.Equal(2, list.Len())

	var buf bytes.Buffer
	list.EncodeIndex(0, &buf)
	want, err := tx1.Marshal()
	r.NoError(err)
	r.Equal(want, buf.Bytes())
}

func TestTransactionsV2ListLenAndEncodeIndex(t *testing.T) {
	r := require.New(t)
	tx1 := newLegacyForAccessors()
	list := TransactionsV2{tx1}
	r.Equal(1, list.Len())

	var buf bytes.Buffer
	list.EncodeIndex(0, &buf)
	want, err := tx1.MarshalV2()
	r.NoError(err)
	r.Equal(want, buf.Bytes())
}

func TestTransactionSetNonce(t *testing.T) {
	r := require.New(t)
	tx := newLegacyForAccessors()
	r.Equal(uint64(1), tx.Nonce())
	tx.SetNonce(42)
	r.Equal(uint64(42), tx.Nonce())
}

func TestTransactionCost(t *testing.T) {
	r := require.New(t)
	tx := newLegacyForAccessors()
	// cost = gasPrice * gas + value = 7*21000 + 100 = 147100
	want := uint256.NewInt(7 * 21000)
	want.Add(want, uint256.NewInt(100))
	r.Equal(want, tx.Cost())
}

func TestTransactionBlobAccessorsOnNonBlobTx(t *testing.T) {
	r := require.New(t)
	tx := newLegacyForAccessors()
	r.Nil(tx.BlobHashes())
	r.Nil(tx.BlobTxSidecar())
	r.Nil(tx.BlobFeeCap())
	r.Equal(uint64(0), tx.BlobGas())
}

func TestTransactionBlobAccessorsOnBlobTx(t *testing.T) {
	r := require.New(t)
	to := types.Address{9}
	blobTx := NewTx(&BlobTx{
		ChainID:    uint256.NewInt(1),
		Nonce:      0,
		GasTipCap:  uint256.NewInt(1),
		GasFeeCap:  uint256.NewInt(2),
		Gas:        21000,
		To:         to,
		Value:      uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(3),
		BlobHashes: []types.Hash{{0xAA}},
		V:          uint256.NewInt(0),
		R:          uint256.NewInt(1),
		S:          uint256.NewInt(1),
	})
	r.Equal([]types.Hash{{0xAA}}, blobTx.BlobHashes())
	r.Equal(uint256.NewInt(3), blobTx.BlobFeeCap())
	r.Equal(uint64(BlobTxBlobGasPerBlob), blobTx.BlobGas())
}

func TestTransactionGasFeeCapCmpAndIntCmp(t *testing.T) {
	r := require.New(t)
	txLow := newLegacyForAccessors() // GasPrice == GasFeeCap for legacy
	to := types.Address{3}
	txHigh := NewTx(&LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(20),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(0),
		V:        uint256.NewInt(37),
		R:        uint256.NewInt(2),
		S:        uint256.NewInt(3),
	})

	r.Equal(-1, txLow.GasFeeCapCmp(txHigh))
	r.Equal(1, txHigh.GasFeeCapCmp(txLow))
	r.Equal(0, txLow.GasFeeCapCmp(txLow))

	r.Equal(-1, txLow.GasFeeCapIntCmp(uint256.NewInt(20)))
	r.Equal(0, txLow.GasFeeCapIntCmp(uint256.NewInt(7)))
}
