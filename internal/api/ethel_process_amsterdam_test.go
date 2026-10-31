package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/bal"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// The eth-el batch replay (ethel.ProcessBlock) rebuilds the EIP-7928 block
// access list of an Amsterdam block and checks it against the header, as
// the engine import does: a block the payload builder made, with a
// zero-amount withdrawal that only lists its recipient, replays, and the
// same block committing to another list is refused.
//
// The recorder reads state of its own, so the block is also replayed from
// the witness stream the executor records: replaying it must read the same
// values in the same order, which re-recording the replay shows. A block
// without transactions is checked too: its start-of-block system calls are
// still pending when the last index begins.
func TestEthelProcessBlockChecksAmsterdamAccessList(t *testing.T) {
	t.Run("transactions", func(t *testing.T) { testEthelProcessBlockAccessList(t, false) })
	t.Run("empty", func(t *testing.T) { testEthelProcessBlockAccessList(t, true) })
}

func testEthelProcessBlockAccessList(t *testing.T, empty bool) {
	bt := newAmsterdamBuildTest(t)
	if empty {
		bt.api.txspool = &mockEngineTxPool{}
	}
	beaconRoot := types.Hash{0x42}
	zeroRecipient := types.Address{0xcc, 0x01}
	withdrawals := []*ethel.Withdrawal{
		{Index: 0, Validator: 1, Address: zeroRecipient, Amount: 0},
		{Index: 1, Validator: 2, Address: types.Address{0xcc, 0x02}, Amount: 5},
	}
	attrs := &PayloadAttributesV1{Timestamp: 2, PrevRandao: types.Hash{0x11}, SuggestedFeeRecipient: bt.coinbase}
	result := bt.engine.buildExecutionPayloadStateful(bt.genesis, bt.headHash, attrs, eestWithdrawals(withdrawals), &beaconRoot)
	require.NotNil(t, result)
	require.Equal(t, empty, len(result.block.Transactions()) == 0)
	header := result.block.Header().(*block.Header)
	require.NotNil(t, header.BlockAccessListHash)
	list, err := bal.Decode(result.blockAccessList)
	require.NoError(t, err)
	listed := false
	for _, acc := range list {
		listed = listed || acc.Address == zeroRecipient
	}
	require.True(t, listed, "the zero withdrawal's recipient must be listed")

	engine := ethel.NewEthReplayEngine(bt.cfg)
	blockHash := func(n uint64) types.Hash {
		if n == 0 {
			return bt.headHash
		}
		return types.Hash{}
	}
	cfg := bt.cfg
	process := func(h *block.Header, reader state.StateReader) (*ethel.BlockResult, error) {
		return ethel.ProcessBlock(cfg, engine, h, result.block.Transactions(), nil, withdrawals,
			state.New(reader), blockHash, nil)
	}

	var stream []byte
	require.NoError(t, bt.api.db.View(context.Background(), func(tx kv.Tx) error {
		recorder := ethel.NewWitnessStateReader(state.NewPlainStateReader(tx))
		res, err := process(header, recorder)
		if err != nil {
			return err
		}
		require.Equal(t, header.GasUsed, res.GasUsed)
		stream = recorder.Encode()

		tampered := block.CopyHeader(header)
		other := crypto.Keccak256Hash([]byte{0xc0})
		tampered.BlockAccessListHash = &other
		_, err = process(tampered, state.NewPlainStateReader(tx))
		require.ErrorContains(t, err, "block access list hash mismatch")

		noHash := block.CopyHeader(header)
		noHash.BlockAccessListHash = nil
		_, err = process(noHash, state.NewPlainStateReader(tx))
		require.ErrorContains(t, err, "block access list hash mismatch")

		replay := ethel.NewWitnessStateReader(ethel.NewWitnessReplayReader(stream, tx))
		res, err = process(header, replay)
		if err != nil {
			return err
		}
		require.Equal(t, header.GasUsed, res.GasUsed)
		require.Equal(t, stream, replay.Encode(), "the replay read another sequence than the recording")

		// N42's own BAL commits to the same header field; a chain that
		// schedules it too checks the spec list.
		both := *bt.cfg
		both.BALTime = big.NewInt(0)
		cfg = &both
		_, err = process(header, state.NewPlainStateReader(tx))
		return err
	}))
	require.NotEmpty(t, stream)
}
