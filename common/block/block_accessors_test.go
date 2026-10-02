package block

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlockAccessors(t *testing.T) {
	r := require.New(t)
	h := sampleRLPHeaders()[1] // london
	h.ResetHashCache()
	blk := NewBlock(h, nil).(*Block)

	r.Equal(h.Number, blk.Number64())
	r.Equal(h.BaseFee, blk.BaseFee64())
	r.Equal(h.Difficulty, blk.Difficulty())
	r.Equal(h.Time, blk.Time())
	r.Equal(h.GasLimit, blk.GasLimit())
	r.Equal(h.GasUsed, blk.GasUsed())
	r.Equal(h.Nonce.Uint64(), blk.Nonce())
	r.Equal(h.Coinbase, blk.Coinbase())
	r.Equal(h.ParentHash, blk.ParentHash())
	r.Equal(h.TxHash, blk.TxHash())

	// Body() returns the attached body interface.
	r.NotNil(blk.Body())

	// Header() returns a defensive copy, not the same pointer.
	hdrCopy := blk.Header()
	r.NotSame(h, hdrCopy)

	// Transaction lookup and Uncles/SendersToTxs are interface stubs.
	r.Nil(blk.Transaction(h.Hash()))
	r.Nil(blk.Uncles())
	blk.SendersToTxs(nil) // must not panic
}

func TestBlockWithSeal(t *testing.T) {
	r := require.New(t)
	h1 := sampleRLPHeaders()[0]
	h1.ResetHashCache()
	blk := NewBlock(h1, nil).(*Block)

	h2 := sampleRLPHeaders()[1]
	h2.ResetHashCache()
	sealed := blk.WithSeal(h2)
	r.Same(blk, sealed)
	r.Equal(h2.GasLimit, blk.GasLimit())
}

func TestBlockToProtoMessageAndFromProtoMessage(t *testing.T) {
	r := require.New(t)
	h := sampleRLPHeaders()[2] // cancun
	h.ResetHashCache()
	blk := NewBlock(h, nil).(*Block)

	pbMsg := blk.ToProtoMessage()
	r.NotNil(pbMsg)

	var blk2 Block
	r.NoError(blk2.FromProtoMessage(pbMsg))
	r.Equal(blk.GasLimit(), blk2.GasLimit())
	r.Equal(blk.Coinbase(), blk2.Coinbase())
	r.False(blk2.ReceiveAt.IsZero())
}

func TestBlockFromProtoMessageWrongType(t *testing.T) {
	r := require.New(t)
	var blk Block
	err := blk.FromProtoMessage(nil)
	r.Error(err)
}

func TestDeferredAt(t *testing.T) {
	r := require.New(t)
	saved := DeferredExecutionTime
	defer func() { DeferredExecutionTime = saved }()

	DeferredExecutionTime = 0
	r.False(DeferredAt(1000))

	DeferredExecutionTime = 500
	r.False(DeferredAt(100))
	r.True(DeferredAt(500))
	r.True(DeferredAt(1000))
}
