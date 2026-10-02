// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_aggregate_synth_test.go covers outputDescription and
// witnessAggregateState.absorb in NoOutput mode (no batcher, no async
// writer channel): out-of-order WitnessResults arriving at the reorder
// buffer must be released strictly in block order and advance a.next.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutputDescription(t *testing.T) {
	require.Equal(t, "(none, smoke run)", outputDescription(WitnessReplayConfig{NoOutput: true}))
	require.Equal(t, "/tmp/out", outputDescription(WitnessReplayConfig{OutputPath: "/tmp/out"}))
}

// TestWitnessAggregateState_Absorb_NoOutput feeds three block results out
// of order (1, 0, 2) into absorb with out==nil (NoOutput mode) and checks
// next advances to end once all three have been released in order.
func TestWitnessAggregateState_Absorb_NoOutput(t *testing.T) {
	a := &witnessAggregateState{
		reorder: newReorderBuffer(8),
		next:    0,
		end:     3,
		ctx:     context.Background(),
	}
	require.NoError(t, a.absorb(WitnessResult{BlockNum: 1}))
	require.EqualValues(t, 0, a.next) // block 0 hasn't arrived yet
	require.NoError(t, a.absorb(WitnessResult{BlockNum: 0}))
	require.EqualValues(t, 2, a.next) // 0 and 1 both released
	require.NoError(t, a.absorb(WitnessResult{BlockNum: 2}))
	require.EqualValues(t, 3, a.next) // reached end
}

// TestWitnessAggregateState_Absorb_WithOutChannel exercises the a.out
// non-nil path: ordered results are forwarded to the channel.
func TestWitnessAggregateState_Absorb_WithOutChannel(t *testing.T) {
	out := make(chan WitnessResult, 4)
	a := &witnessAggregateState{
		reorder: newReorderBuffer(8),
		next:    0,
		end:     2,
		out:     out,
		ctx:     context.Background(),
	}
	require.NoError(t, a.absorb(WitnessResult{BlockNum: 0}))
	require.NoError(t, a.absorb(WitnessResult{BlockNum: 1}))
	close(out)
	var got []uint64
	for r := range out {
		got = append(got, r.BlockNum)
	}
	require.Equal(t, []uint64{0, 1}, got)
}

// TestWitnessAggregateState_Absorb_CtxCanceled checks absorb returns the
// context error when the out channel send would block and ctx is done.
func TestWitnessAggregateState_Absorb_CtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := make(chan WitnessResult) // unbuffered, nobody reading
	a := &witnessAggregateState{
		reorder: newReorderBuffer(8),
		next:    0,
		end:     2,
		out:     out,
		ctx:     ctx,
	}
	err := a.absorb(WitnessResult{BlockNum: 0})
	require.Error(t, err)
}
