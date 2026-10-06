package internal

// coreT_write_block_with_td_test.go covers writeBlockWithTd's success path
// (header/body + Td persisted and cached) and its insertion-interrupted
// short circuit.

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestCoreTWriteBlockWithTd_Success(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	require.NotEmpty(t, f.Blocks)

	blk := f.Blocks[0]
	wantTd := uint256.NewInt(123456)

	err := bc.writeBlockWithTd(blk, wantTd)
	require.NoError(t, err)

	roTx, rerr := f.DB.BeginRo(bc.ctx)
	require.NoError(t, rerr)
	defer roTx.Rollback()

	got, gerr := rawdb.ReadTd(roTx, blk.Hash(), blk.Number64().Uint64())
	require.NoError(t, gerr)
	require.Equal(t, wantTd.ToBig(), got.ToBig())

	stored := rawdb.ReadBlock(roTx, blk.Hash(), blk.Number64().Uint64())
	require.NotNil(t, stored)
	require.Equal(t, blk.Hash(), stored.Hash())

	cached, ok := bc.tdCache.Get(blk.Hash())
	require.True(t, ok)
	require.Equal(t, wantTd.ToBig(), cached.ToBig())
}

func TestCoreTWriteBlockWithTd_InsertionInterrupted(t *testing.T) {
	f := coreTNewChainFixture(t)
	bc := f.Chain
	require.NotEmpty(t, f.Blocks)

	bc.StopInsert()
	err := bc.writeBlockWithTd(f.Blocks[0], uint256.NewInt(1))
	require.ErrorIs(t, err, errInsertionInterrupted)
}
