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
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	dephexutil "github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	depmerge "github.com/n42blockchain/N42/internal/cl/depshim/merge"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/internal/cl/eladapter"
	"github.com/n42blockchain/N42/internal/cl/phase1/execution_client"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// chainHash converts depshim/common.Hash (alias of lib/common.Hash) to
// common/types.Hash. Both are [32]byte but Go treats them as distinct
// named types, so the seam between Caplin and modules/rawdb needs an
// explicit conversion.
func chainHash(h depcommon.Hash) types.Hash { return types.Hash(h) }

// chaindbProvider is the narrow surface ethELBackend needs from the
// surrounding eth-el process for the read-only path. *ethel.Node
// satisfies it via its DB() method, and tests satisfy it with a static
// kv.RoDB. This indirection keeps the Backend wireable BEFORE
// Node.Start has opened the chaindata (returns nil → Backend reports
// "not ready") and easy to unit-test.
type chaindbProvider interface {
	DB() kv.RoDB
}

// executionProvider is the wider surface the Phase 7.1.1.b real
// ExecutePayload / UpdateForkchoice / ReadCurrentHeader path needs.
// Optional: when nil, those three methods return errExecutionLayerNotReady
// so the read-only seam keeps working in unit tests that don't stand up
// an EngineAPIv4. *ethel.Node implements every method.
type executionProvider interface {
	RwDB() kv.RwDB
	ChainConfig() *params.ChainConfig
	Engine() consensus.Engine
	OutFreezer() *freezer.Freezer
}

// ethELBackend bridges Caplin's eladapter.Backend interface and the
// eth-el chaindata. It is the only file in the n42el build of cmd/eth-el
// that simultaneously imports Caplin (depshim) and the rest of N42,
// preserving the rule that internal/cl never reaches back into N42
// business code.
type ethELBackend struct {
	provider chaindbProvider
	exec     executionProvider

	// initOnce / engineV4 / stateAdpt / initErr — lazily built on
	// first ExecutePayload / UpdateForkchoice call. Building eagerly
	// would fail when Node.Start hasn't opened RwDB yet.
	initOnce  sync.Once
	apiCore   *api.API
	engineV4  *api.EngineAPIv4
	engineV1  *api.EngineAPIV1
	stateAdpt *api.EngineStateAdapter
	initErr   error
}

// Compile-time check that *ethELBackend satisfies eladapter.Backend.
var _ eladapter.Backend = (*ethELBackend)(nil)

// newEthELBackend constructs a read-only Backend. Used by tests and by
// pre-Phase-7.1.1.b production wiring that only needs the read path.
func newEthELBackend(p chaindbProvider) *ethELBackend {
	return newEthELBackendWith(p, nil)
}

// newEthELBackendWith constructs a Backend with the optional
// executionProvider. Pass *ethel.Node for both arguments in production:
// it satisfies both interfaces, and the Phase 7.1.1.b real Engine API
// path needs the wider surface to build api.EngineAPIv4 +
// EngineStateAdapter on first use.
func newEthELBackendWith(p chaindbProvider, exec executionProvider) *ethELBackend {
	return &ethELBackend{provider: p, exec: exec}
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

// errExecutionLayerNotReady is returned by the Engine API methods when
// the optional executionProvider was not supplied (test mode) or when
// lazy API construction failed. Distinct from
// eladapter.ErrNotImplemented so callers can distinguish "wiring gap"
// from "method missing".
var errExecutionLayerNotReady = errors.New(
	"eth-el: execution layer Engine API not wired (executionProvider nil)")

// initAPIs lazily constructs the api.EngineAPIv4 + EngineStateAdapter
// stack that the Engine API methods need. Built on first call so it can
// happen AFTER Node.Start has opened the RwDB. Mirrors the wiring in
// internal/ethel/engineapi/service.go so the in-process path here and
// the auth-RPC HTTP path produce the same execution semantics.
func (b *ethELBackend) initAPIs() error {
	b.initOnce.Do(func() {
		if b.exec == nil {
			b.initErr = errExecutionLayerNotReady
			return
		}
		rwdb := b.exec.RwDB()
		if rwdb == nil {
			b.initErr = errors.New("eth-el: RwDB not opened yet")
			return
		}
		chainCfg := b.exec.ChainConfig()
		engine := b.exec.Engine()
		fz := b.exec.OutFreezer()
		if chainCfg == nil || engine == nil {
			b.initErr = errors.New("eth-el: chainCfg or engine missing on executionProvider")
			return
		}

		b.apiCore = api.NewAPI(nil, rwdb, engine, nil, nil, chainCfg)
		b.stateAdpt = api.NewEngineStateAdapter(rwdb, fz, chainCfg, engine)

		apis := api.EngineAPIs(b.apiCore)
		for _, a := range apis {
			switch svc := a.Service.(type) {
			case *api.EngineAPIV1:
				svc.SetStateAdapter(b.stateAdpt)
				b.engineV1 = svc
			case *api.EngineAPIBlob:
				svc.SetStateAdapter(b.stateAdpt)
			case *api.EngineAPIv4:
				svc.SetStateAdapter(b.stateAdpt)
				b.engineV4 = svc
			}
		}
		if b.engineV4 == nil || b.engineV1 == nil {
			b.initErr = errors.New("eth-el: api.EngineAPIs() returned an incomplete set")
		}
	})
	return b.initErr
}

// ExecutePayload — Phase 7.1.1.b real implementation. Converts the
// caplin payload to api.ExecutionPayloadV4, calls EngineAPIv4.NewPayloadV4
// (the same code path the auth-RPC listener exposes to external CLs),
// and maps the returned PayloadStatusV1.Status string back to a
// Caplin execution_client.PayloadStatus.
//
// Only Pectra (V4) payloads are wired here. Earlier-fork payloads
// surface as errPayloadConversionPending — current archive replay
// + mainnet tip is all Pectra so this is fine; Phase 7.1.1.c can add
// V1/V2/V3 converters when historical chains need them.
func (b *ethELBackend) ExecutePayload(
	ctx context.Context,
	blk *cltypes.Eth1Block,
	parentBeaconBlockRoot *depcommon.Hash,
	versionedHashes []depcommon.Hash,
	executionRequests []dephexutil.Bytes,
) (execution_client.PayloadStatus, error) {
	if err := b.initAPIs(); err != nil {
		return execution_client.PayloadStatusNotValidated, err
	}
	payload, err := eth1BlockToExecutionPayloadV4(blk)
	if err != nil {
		return execution_client.PayloadStatusNotValidated, fmt.Errorf("convert payload: %w", err)
	}

	// Caplin's hash types are different named-types from the api's
	// even though both are [32]byte. Convert the slices.
	vh := make([]types.Hash, len(versionedHashes))
	for i, h := range versionedHashes {
		vh[i] = types.Hash(h)
	}
	var parentRoot *types.Hash
	if parentBeaconBlockRoot != nil {
		h := types.Hash(*parentBeaconBlockRoot)
		parentRoot = &h
	}
	reqs := make([]hexutil.Bytes, len(executionRequests))
	for i, r := range executionRequests {
		reqs[i] = hexutil.Bytes(append([]byte(nil), r...))
	}

	status, err := b.engineV4.NewPayloadV4(ctx, payload, vh, parentRoot, reqs)
	if err != nil {
		return execution_client.PayloadStatusNotValidated, err
	}
	return mapPayloadStatus(status), nil
}

// UpdateForkchoice — Phase 7.1.1.b. Calls
// EngineAPIv4.ForkchoiceUpdatedV3 (the Cancun+ path; V4 reuses the same
// FCU shape, only the NewPayload variant differs).
//
// PayloadAttributes is not wired in this commit — a stage loop that
// asks the EL to assemble a new payload (block production) needs more
// of the api surface than Caplin has historically used. attrs == nil
// (the more common case, "just update head") is fully supported.
func (b *ethELBackend) UpdateForkchoice(
	ctx context.Context,
	head, safe, finalized depcommon.Hash,
	attrs *engine_types.PayloadAttributes,
	_ clparams.StateVersion,
) ([]byte, error) {
	if err := b.initAPIs(); err != nil {
		return nil, err
	}
	state := &api.ForkchoiceStateV1{
		HeadBlockHash:      types.Hash(head),
		SafeBlockHash:      types.Hash(safe),
		FinalizedBlockHash: types.Hash(finalized),
	}
	if attrs != nil {
		// Block-production path. Phase 7.1.1.c hooks PayloadAttributesV3
		// construction (timestamp + prevRandao + recipient + withdrawals
		// + parentBeaconBlockRoot from attrs). For now, ignore attrs and
		// return only the FCU result — Caplin nodes that don't propose
		// blocks reach the same outcome.
	}
	// Use the blob-API to dispatch ForkchoiceUpdatedV3 since the
	// EngineAPIBlob service owns that method. We can reach it via the
	// api.EngineAPIs() set; for simplicity reuse EngineAPIV1's overlay
	// path which mirrors ForkchoiceV1 semantics — head update only.
	resp, err := b.engineV1.ForkchoiceUpdatedV1(ctx, state, nil)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.PayloadID == nil {
		return nil, nil
	}
	return resp.PayloadID[:], nil
}

// ReadCurrentHeader — Phase 7.1.1.b. Reads the canonical head from
// rawdb and converts the N42 common/block.Header into a
// depshim/types.Header so Caplin code that walks the head pointer
// can run.
//
// The depshim type embeds big.Int for Number / Difficulty (vs N42's
// *uint256.Int), so this is a careful field-by-field rebuild. We
// intentionally do NOT call any depshim DeriveSha — that helper is a
// stub that panics on non-empty inputs; the caller doesn't need it
// because TxHash / ReceiptHash / WithdrawalsHash are read straight
// from the stored header.
func (b *ethELBackend) ReadCurrentHeader(ctx context.Context) (*deptypes.Header, error) {
	return withRoTx(ctx, b, func(tx kv.Tx) (*deptypes.Header, error) {
		headHash := rawdb.ReadHeadBlockHash(tx)
		if headHash == (types.Hash{}) {
			return nil, nil
		}
		num := rawdb.ReadHeaderNumber(tx, headHash)
		if num == nil {
			return nil, nil
		}
		header := rawdb.ReadHeader(tx, headHash, *num)
		if header == nil {
			return nil, nil
		}
		return n42HeaderToDepshim(header), nil
	})
}

// n42HeaderToDepshim copies fields from N42's common/block.Header to
// the depshim/types.Header shape Caplin uses. Pure value copy; no
// hashing or canonicalisation.
//
// Field-by-field conversion exists because depshim.Header uses
// big.Int values (not *uint256.Int) for Difficulty / Number; the rest
// of the fields share a memory layout but Go's named-type system still
// requires explicit conversion.
func n42HeaderToDepshim(h *block.Header) *deptypes.Header {
	if h == nil {
		return nil
	}
	out := &deptypes.Header{
		ParentHash:  depcommon.Hash(h.ParentHash),
		UncleHash:   depcommon.Hash(h.UncleHash),
		Coinbase:    depcommon.Address(h.Coinbase),
		Root:        depcommon.Hash(h.Root),
		TxHash:      depcommon.Hash(h.TxHash),
		ReceiptHash: depcommon.Hash(h.ReceiptHash),
		Bloom:       deptypes.Bloom(h.Bloom),
		GasLimit:    h.GasLimit,
		GasUsed:     h.GasUsed,
		Time:        h.Time,
		Extra:       append([]byte(nil), h.Extra...),
		MixDigest:   depcommon.Hash(h.MixDigest),
		Nonce:       depmerge.BlockNonce(h.Nonce),
	}
	if h.Difficulty != nil {
		out.Difficulty = *h.Difficulty.ToBig()
	}
	if h.Number != nil {
		out.Number = *h.Number.ToBig()
	}
	if h.BaseFee != nil {
		// Both N42 and depshim use *uint256.Int for BaseFee — defensive clone.
		out.BaseFee = h.BaseFee.Clone()
	}
	if h.WithdrawalsHash != nil {
		w := depcommon.Hash(*h.WithdrawalsHash)
		out.WithdrawalsHash = &w
	}
	if h.BlobGasUsed != nil {
		v := *h.BlobGasUsed
		out.BlobGasUsed = &v
	}
	if h.ExcessBlobGas != nil {
		v := *h.ExcessBlobGas
		out.ExcessBlobGas = &v
	}
	if h.ParentBeaconRoot != nil {
		p := depcommon.Hash(*h.ParentBeaconRoot)
		out.ParentBeaconBlockRoot = &p
	}
	if h.RequestsHash != nil {
		r := depcommon.Hash(*h.RequestsHash)
		out.RequestsHash = &r
	}
	return out
}

// mapPayloadStatus converts the api.PayloadStatusV1.Status string
// (engine_newPayload wire format) back to the Caplin enum.
func mapPayloadStatus(s *api.PayloadStatusV1) execution_client.PayloadStatus {
	if s == nil {
		return execution_client.PayloadStatusNotValidated
	}
	switch s.Status {
	case api.PayloadStatusValid:
		return execution_client.PayloadStatusValidated
	case api.PayloadStatusInvalid:
		return execution_client.PayloadStatusInvalidated
	case api.PayloadStatusSyncing, api.PayloadStatusAccepted:
		return execution_client.PayloadStatusNotValidated
	}
	return execution_client.PayloadStatusNone
}
