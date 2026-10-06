package api

// apix_agg_sign_test.go exercises agg_sign.go's MachineVerify (no verifiers
// registered path) and SignMerge (context-cancelled, no signatures
// collected path), both of which are pure control flow that does not touch
// the executed-chain fixture.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

func TestMachineVerifyNoVerifiersReturnsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := MachineVerify(ctx)
	require.NoError(t, err)
}

func TestSignMergeNoSignaturesCollectedErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	header := &block.Header{Number: apiXUint256(1)}
	_, _, err := SignMerge(ctx, header, 0)
	require.ErrorIs(t, err, consensus.ErrNotEnoughSign)
}

func TestAggSignCheckRootMismatch(t *testing.T) {
	var s AggSign
	require.False(t, s.Check(types.Hash{0x01}))
}
