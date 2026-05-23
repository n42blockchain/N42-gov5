// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42el-tagged beacon backend for the eth-el binary.
// Adapts internal/ethel.Node to the Caplin eladapter.Backend
// interface through the chaindbProvider seam and the chainHash
// helper that converts depshim Hash to common/types.Hash. Guarded
// by the n42el build tag so non-EL builds ship without pulling
// the internal/cl subtree.

//go:build n42el

package main

import (
	"context"
	"errors"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	"github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/internal/cl/eladapter"
	"github.com/n42blockchain/N42/internal/cl/phase1/execution_client"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// chainHash converts depshim/common.Hash (alias of lib/common.Hash) to
// common/types.Hash. Both are [32]byte but Go treats them as distinct
// named types, so the seam between Caplin and modules/rawdb needs an
// explicit conversion.
func chainHash(h depcommon.Hash) types.Hash { return types.Hash(h) }

// chaindbProvider is the narrow surface ethELBackend needs from the
// surrounding eth-el process. *ethel.Node satisfies it via its DB()
// method, and tests satisfy it with a static kv.RoDB. This indirection
// keeps the Backend wireable BEFORE Node.Start has opened the chaindata
// (returns nil → Backend reports "not ready") and easy to unit-test.
type chaindbProvider interface {
	DB() kv.RoDB
}

// ethELBackend bridges Caplin's eladapter.Backend interface and the
// eth-el chaindata. It is the only file in the n42el build of cmd/eth-el
// that simultaneously imports Caplin (depshim) and the rest of N42,
// preserving the rule that internal/cl never reaches back into N42
// business code.
type ethELBackend struct {
	provider chaindbProvider
}

// Compile-time check that *ethELBackend satisfies eladapter.Backend.
var _ eladapter.Backend = (*ethELBackend)(nil)

func newEthELBackend(p chaindbProvider) *ethELBackend {
	return &ethELBackend{provider: p}
}

func (b *ethELBackend) chaindb() kv.RoDB {
	if b.provider == nil {
		return nil
	}
	return b.provider.DB()
}

// withRoTx opens a fresh read-only transaction on chaindata, runs fn, and
// rolls it back. Returns the zero value of T when the chaindata is not
// yet open. Per-call BeginRo is intentional: a long-lived RO tx pins the
// snapshot and prevents writer free-list reclamation.
func withRoTx[T any](ctx context.Context, b *ethELBackend, fn func(kv.Tx) (T, error)) (T, error) {
	var zero T
	db := b.chaindb()
	if db == nil {
		return zero, nil
	}
	tx, err := db.BeginRo(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	return fn(tx)
}

func (b *ethELBackend) CurrentHeadNumber(ctx context.Context) (uint64, error) {
	return withRoTx(ctx, b, func(tx kv.Tx) (uint64, error) {
		headHash := rawdb.ReadHeadBlockHash(tx)
		if headHash == (types.Hash{}) {
			return 0, nil
		}
		num := rawdb.ReadHeaderNumber(tx, headHash)
		if num == nil {
			return 0, nil
		}
		return *num, nil
	})
}

func (b *ethELBackend) HasBlock(ctx context.Context, hash depcommon.Hash) (bool, error) {
	return withRoTx(ctx, b, func(tx kv.Tx) (bool, error) {
		return rawdb.ReadHeaderNumber(tx, chainHash(hash)) != nil, nil
	})
}

func (b *ethELBackend) IsCanonicalHash(ctx context.Context, hash depcommon.Hash) (bool, error) {
	return withRoTx(ctx, b, func(tx kv.Tx) (bool, error) {
		ch := chainHash(hash)
		num := rawdb.ReadHeaderNumber(tx, ch)
		if num == nil {
			return false, nil
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *num)
		if err != nil {
			return false, err
		}
		return canonical == ch, nil
	})
}

func (b *ethELBackend) Ready(_ context.Context) (bool, error) {
	return b.chaindb() != nil, nil
}

// errPayloadConversionPending is returned by ExecutePayload /
// UpdateForkchoice until Phase 7.1.1.b lands the cltypes.Eth1Block →
// common/block.Block converter and wires the EngineStateAdapter call.
// It is intentionally distinct from eladapter.ErrNotImplemented so the
// stage loop (Phase 7.3+) can distinguish "method wired but converter
// pending" from "interface method missing entirely".
var errPayloadConversionPending = errors.New(
	"eth-el: payload converter not wired yet (Phase 7.1.1.b)")

// ExecutePayload — Phase 7.1.1.a scaffold. Signature is the final
// production signature; body is a stub until Phase 7.1.1.b adds the
// cltypes.Eth1Block → common/block.Block converter and calls
// internal/api.EngineStateAdapter.ExecutePayload.
//
// Returning PayloadStatusNotValidated keeps Caplin's stage loop on a
// safe path (no false ValidatedStatus); the error is propagated so
// upstream callers see why the stage stalled.
func (b *ethELBackend) ExecutePayload(
	_ context.Context,
	_ *cltypes.Eth1Block,
	_ *depcommon.Hash,
	_ []depcommon.Hash,
	_ []hexutil.Bytes,
) (execution_client.PayloadStatus, error) {
	return execution_client.PayloadStatusNotValidated, errPayloadConversionPending
}

// UpdateForkchoice — Phase 7.1.1.a scaffold. Signature is final;
// body stub until Phase 7.1.1.b wires
// internal/api.EngineStateAdapter.ForkchoiceUpdated.
func (b *ethELBackend) UpdateForkchoice(
	_ context.Context,
	_, _, _ depcommon.Hash,
	_ *engine_types.PayloadAttributes,
	_ clparams.StateVersion,
) ([]byte, error) {
	return nil, errPayloadConversionPending
}

// ReadCurrentHeader — Phase 7.1.1.a scaffold. Reads chaindata for the
// canonical head hash + header number; depshim/types.Header construction
// from N42's common/block.Header is left for Phase 7.1.1.b.
//
// Returning (nil, nil) signals "no head known" to Caplin, which treats
// that as a startup race rather than an error.
func (b *ethELBackend) ReadCurrentHeader(ctx context.Context) (*deptypes.Header, error) {
	return withRoTx(ctx, b, func(tx kv.Tx) (*deptypes.Header, error) {
		headHash := rawdb.ReadHeadBlockHash(tx)
		if headHash == (types.Hash{}) {
			return nil, nil
		}
		// Phase 7.1.1.b: convert N42 common/block.Header → depshim/types.Header
		// (the depshim DeriveSha stub panics on non-empty tx lists, so the
		// converter has to fill in the real MPT root computed from the body).
		return nil, errPayloadConversionPending
	})
}
