package api

// aggSignT_test.go covers the remaining branches of agg_sign.go: AggSign.Check
// with a genuinely valid BLS signature, SignMerge's happy-path aggregation
// over sigChannel, and MachineVerify's no-verifiers-registered wait/cancel
// branch (the only reachable branch today: nothing populates validVerifiers
// at runtime, per the function's own doc comment).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

func aggSignTNewSigned(t *testing.T, root types.Hash, addr types.Address) AggSign {
	t.Helper()
	sk, err := bls.RandKey()
	require.NoError(t, err)
	sig := sk.Sign(root[:])

	var s AggSign
	s.StateRoot = root
	copy(s.Sign[:], sig.Marshal())
	copy(s.PublicKey[:], sk.PublicKey().Marshal())
	s.Address = addr
	s.Number = 1
	return s
}

func TestAggSignCheckValidSignature(t *testing.T) {
	root := types.Hash{0x42}
	s := aggSignTNewSigned(t, root, types.Address{0x01})
	require.True(t, s.Check(root))
	// A different root must fail verification.
	require.False(t, s.Check(types.Hash{0x43}))
}

func TestSignMergeAggregatesThreeValidSigns(t *testing.T) {
	header := &block.Header{Number: uint256.NewInt(1), Root: types.Hash{0x7}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for i := 0; i < 3; i++ {
			s := aggSignTNewSigned(t, header.Root, types.Address{byte(i + 1)})
			s.Number = 1
			sigChannel <- s
		}
		// SignMerge's loop only exits via ctx.Done(); cancel once the
		// threshold-worth of signs have been enqueued and should have been
		// drained (give the consumer a moment).
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	sign, verifiers, err := SignMerge(ctx, header, 10)
	require.NoError(t, err)
	require.Len(t, verifiers, 3)
	require.NotEqual(t, types.Signature{}, sign)
}

func TestSignMergeDiscardsWrongNumberAndDuplicateAndBadCheck(t *testing.T) {
	header := &block.Header{Number: uint256.NewInt(5), Root: types.Hash{0x9}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		// Wrong block number: discarded.
		wrong := aggSignTNewSigned(t, header.Root, types.Address{0xaa})
		wrong.Number = 999
		sigChannel <- wrong

		// Bad signature check (root mismatch): discarded.
		bad := aggSignTNewSigned(t, types.Hash{0xde, 0xad}, types.Address{0xbb})
		bad.Number = 5
		sigChannel <- bad

		// Three valid, unique signers reach the threshold.
		for i := 0; i < 3; i++ {
			s := aggSignTNewSigned(t, header.Root, types.Address{byte(0x10 + i)})
			s.Number = 5
			sigChannel <- s
		}
		// Duplicate address: discarded (uniq map hit).
		dup := aggSignTNewSigned(t, header.Root, types.Address{0x10})
		dup.Number = 5
		sigChannel <- dup

		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	sign, verifiers, err := SignMerge(ctx, header, 10)
	require.NoError(t, err)
	require.Len(t, verifiers, 3)
	require.NotEqual(t, types.Signature{}, sign)
}

func TestSignMergeContextCanceledBeforeThreshold(t *testing.T) {
	header := &block.Header{Number: uint256.NewInt(2), Root: types.Hash{0x1}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := SignMerge(ctx, header, 10)
	require.Error(t, err)
}

func TestMachineVerifyNoVerifiersWaitsForCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := MachineVerify(ctx)
	require.NoError(t, err)
}
