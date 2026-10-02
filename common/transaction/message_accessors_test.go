package transaction

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func TestNewMessageAndAccessors(t *testing.T) {
	r := require.New(t)
	from := types.Address{1}
	to := types.Address{2}
	amount := uint256.NewInt(100)
	gasPrice := uint256.NewInt(5)
	feeCap := uint256.NewInt(10)
	tip := uint256.NewInt(2)
	blobFeeCap := uint256.NewInt(3)
	blobHashes := []types.Hash{{0xAA}}
	data := []byte{0xDE, 0xAD}
	al := AccessList{}

	m := NewMessage(from, &to, 7, amount, 21000, gasPrice, feeCap, tip, blobFeeCap, blobHashes, data, al, true, false)

	r.Equal(from, m.From())
	r.Equal(&to, m.To())
	r.Equal(gasPrice.ToBig(), m.GasPrice().ToBig())
	r.Equal(feeCap.ToBig(), m.FeeCap().ToBig())
	r.Equal(tip.ToBig(), m.Tip().ToBig())
	r.Equal(blobFeeCap.ToBig(), m.BlobFeeCap().ToBig())
	r.Equal(blobHashes, m.BlobHashes())
	r.Equal(amount.ToBig(), m.Value().ToBig())
	r.Equal(uint64(21000), m.Gas())
	r.Equal(uint64(7), m.Nonce())
	r.Equal(data, m.Data())
	r.Equal(al, m.AccessList())
	r.Nil(m.AuthList())
	r.True(m.CheckNonce())
	r.False(m.IsFree())
}

func TestNewMessageNilOptionalFields(t *testing.T) {
	r := require.New(t)
	from := types.Address{1}
	amount := uint256.NewInt(0)
	m := NewMessage(from, nil, 0, amount, 0, nil, nil, nil, nil, nil, nil, nil, false, true)

	r.Nil(m.To())
	r.Nil(m.BlobFeeCap())
	r.Equal(uint256.NewInt(0), m.GasPrice())
	r.Equal(uint256.NewInt(0), m.FeeCap())
	r.Equal(uint256.NewInt(0), m.Tip())
	r.Empty(m.BlobHashes())
	r.False(m.CheckNonce())
	r.True(m.IsFree())
}

func TestMessageExecutionValues(t *testing.T) {
	r := require.New(t)
	from := types.Address{1}
	amount := uint256.NewInt(50)
	gasPrice := uint256.NewInt(1)
	feeCap := uint256.NewInt(2)
	tip := uint256.NewInt(3)
	m := NewMessage(from, nil, 0, amount, 0, gasPrice, feeCap, tip, nil, nil, nil, nil, false, false)

	gp, fc, tp, val := m.ExecutionValues()
	r.Equal(gasPrice.ToBig(), gp.ToBig())
	r.Equal(feeCap.ToBig(), fc.ToBig())
	r.Equal(tip.ToBig(), tp.ToBig())
	r.Equal(amount.ToBig(), val.ToBig())

	// Mutating through the returned pointer reflects on the Message itself.
	gp.SetUint64(999)
	r.Equal(uint64(999), m.GasPrice().Uint64())
}

func TestMessageSetCheckNonceAndIsFree(t *testing.T) {
	r := require.New(t)
	from := types.Address{1}
	m := NewMessage(from, nil, 0, uint256.NewInt(0), 0, nil, nil, nil, nil, nil, nil, nil, false, false)
	r.False(m.CheckNonce())
	r.False(m.IsFree())

	m.SetCheckNonce(true)
	r.True(m.CheckNonce())

	m.SetIsFree(true)
	r.True(m.IsFree())
}

func TestCopyUint256OrZero(t *testing.T) {
	r := require.New(t)
	zero := copyUint256OrZero(nil)
	r.True(zero.IsZero())

	v := uint256.NewInt(123)
	got := copyUint256OrZero(v)
	r.Equal(v.ToBig(), got.ToBig())
	// Must be a copy, not an alias.
	v.SetUint64(456)
	r.Equal(uint64(123), got.Uint64())
}
