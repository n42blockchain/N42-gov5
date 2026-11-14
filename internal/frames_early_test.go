package internal

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func framesEarlyTestBlock(n uint64) *block.Block {
	h := &block.Header{Number: uint256.NewInt(n)}
	body := &block.Body{
		FrameHashes: []types.Hash{{byte(n), 1}, {byte(n), 2}},
		FrameData:   [][]byte{{1, 2, 3}, {4, 5, 6}},
	}
	return block.NewBlockFromStorage(h.Hash(), h, body)
}

// framesEarlyTestWrites counts the store writes per first frame hash and
// fails them with err.
func framesEarlyTestWrites(t *testing.T, err error) (*sync.Map, func()) {
	t.Helper()
	counts := &sync.Map{}
	restoreStore := rawdb.SetFrameStore(memdb.NewTestDB(t))
	prev := framesWriteFn
	framesWriteFn = func(_ context.Context, hashes []types.Hash, _ [][]byte, _ *rawdb.FrameWriteTimes) error {
		c, _ := counts.LoadOrStore(hashes[0], new(atomic.Int32))
		c.(*atomic.Int32).Add(1)
		return err
	}
	return counts, func() { framesWriteFn = prev; restoreStore() }
}

func framesEarlyCount(counts *sync.Map, b *block.Block) int32 {
	body := b.Body().(*block.Body)
	if c, ok := counts.Load(body.FrameHashes[0]); ok {
		return c.(*atomic.Int32).Load()
	}
	return 0
}

// The hand-off takes the early write: one store write per block.
func TestFramesWriteEarlyReused(t *testing.T) {
	counts, restore := framesEarlyTestWrites(t, nil)
	defer restore()
	defer SetFramesWriteEarlyForTest(true)()
	bc := &BlockChain{}
	b := framesEarlyTestBlock(1)
	s0, r0 := framesEarlyStarted.Load(), framesEarlyReused.Load()
	bc.framesEarly.start(b)
	bc.framesEarly.start(b) // a second enqueue starts nothing
	fw := bc.framesEarly.take(b)
	require.NotNil(t, fw)
	require.True(t, fw.early)
	require.NoError(t, fw.wait())
	require.Nil(t, bc.framesEarly.take(b), "taken once")
	require.Equal(t, int32(1), framesEarlyCount(counts, b))
	require.Equal(t, s0+1, framesEarlyStarted.Load())
	require.Equal(t, r0+1, framesEarlyReused.Load())
}

// An early write's error comes out of the write the hand-off waits on.
func TestFramesWriteEarlyError(t *testing.T) {
	want := errors.New("disk full")
	_, restore := framesEarlyTestWrites(t, want)
	defer restore()
	bc := &BlockChain{}
	b := framesEarlyTestBlock(2)
	bc.framesEarly.start(b)
	fw := bc.framesEarly.take(b)
	require.NotNil(t, fw)
	require.ErrorIs(t, fw.wait(), want)
}

// The map is bounded, FIFO, and an evicted block's hand-off starts its own.
func TestFramesWriteEarlyBounded(t *testing.T) {
	counts, restore := framesEarlyTestWrites(t, nil)
	defer restore()
	bc := &BlockChain{}
	var blocks []*block.Block
	for i := 0; i < framesEarlyCap+3; i++ {
		b := framesEarlyTestBlock(uint64(10 + i))
		blocks = append(blocks, b)
		bc.framesEarly.start(b)
	}
	require.Len(t, bc.framesEarly.m, framesEarlyCap)
	require.Nil(t, bc.framesEarly.take(blocks[0]), "oldest evicted")
	fw := bc.framesEarly.take(blocks[len(blocks)-1])
	require.NotNil(t, fw)
	require.NoError(t, fw.wait())
	// The evicted write still ran once; the hand-off falls back to its own.
	fb := startFramesWrite(blocks[0])
	require.NoError(t, fb.wait())
	require.False(t, fb.early)
	require.Eventually(t, func() bool { return framesEarlyCount(counts, blocks[0]) == 2 }, 5*time.Second, time.Millisecond)
	// Let every remembered write finish before the store is restored.
	for _, b := range blocks[1 : len(blocks)-1] {
		if w := bc.framesEarly.take(b); w != nil {
			require.NoError(t, w.wait())
		}
	}
}

// With the switch off the enqueue starts nothing (the hand-off starts its
// own write, as before).
func TestFramesWriteEarlyOff(t *testing.T) {
	_, restore := framesEarlyTestWrites(t, nil)
	defer restore()
	defer SetFramesWriteEarlyForTest(false)()
	bc := &BlockChain{}
	b := framesEarlyTestBlock(3)
	s0 := framesEarlyStarted.Load()
	bc.framesEarly.onEnqueue(b)
	require.Nil(t, bc.framesEarly.take(b))
	require.Equal(t, s0, framesEarlyStarted.Load())
}

// With the switch on the queue's hook starts the write.
func TestFramesWriteEarlyOnEnqueue(t *testing.T) {
	counts, restore := framesEarlyTestWrites(t, nil)
	defer restore()
	defer SetFramesWriteEarlyForTest(true)()
	bc := &BlockChain{}
	b := framesEarlyTestBlock(4)
	bc.framesEarly.onEnqueue(b)
	fw := bc.framesEarly.take(b)
	require.NotNil(t, fw)
	require.NoError(t, fw.wait())
	require.Equal(t, int32(1), framesEarlyCount(counts, b))
}
