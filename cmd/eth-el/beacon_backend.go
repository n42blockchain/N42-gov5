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

	"github.com/n42blockchain/N42/common/types"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/eladapter"
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
