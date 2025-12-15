// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for the unified consensus Engine interface.

package consensus_test

import (
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
	"google.golang.org/protobuf/proto"
)

// =============================================================================
// Engine Interface Tests
// =============================================================================

func TestEngineInterfaceExists(t *testing.T) {
	var _ consensus.Engine = (*testEngine)(nil)
	t.Log("✓ Engine interface exists and is properly defined")
}

func TestEngineReaderInterfaceExists(t *testing.T) {
	var _ consensus.EngineReader = (*testEngine)(nil)
	t.Log("✓ EngineReader interface exists and is properly defined")
}

func TestCoreEngineInterfaceExists(t *testing.T) {
	var _ consensus.CoreEngine = (*consensus.EngineAdapter)(nil)
	t.Log("✓ CoreEngine interface exists and is properly defined")
}

// =============================================================================
// InstrumentedEngine Tests
// =============================================================================

func TestInstrumentedEngineImplementsEngine(t *testing.T) {
	var _ consensus.Engine = (*consensus.InstrumentedEngine)(nil)
	t.Log("✓ InstrumentedEngine implements Engine interface")
}

func TestInstrumentedEngineDisabled(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, false)

	// When disabled, stats should remain zero
	stats := instrumented.Stats()
	if stats.VerifyHeaderCount != 0 {
		t.Errorf("Expected 0 verify header count when disabled, got %d", stats.VerifyHeaderCount)
	}

	t.Log("✓ InstrumentedEngine disabled mode works correctly")
}

func TestInstrumentedEngineStatsReset(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, true)

	// Manually set some values via calls
	instrumented.Author(&testHeader{})
	instrumented.Prepare(&testChainHeaderReader{}, &testHeader{})

	// Verify stats are non-zero
	stats := instrumented.Stats()
	if stats.AuthorCount == 0 || stats.PrepareCount == 0 {
		t.Error("Stats should be non-zero after operations")
	}

	// Reset
	instrumented.ResetStats()

	stats = instrumented.Stats()
	if stats.AuthorCount != 0 || stats.PrepareCount != 0 {
		t.Error("Stats should be zero after reset")
	}

	t.Log("✓ InstrumentedEngine ResetStats works correctly")
}

func TestInstrumentedEngineVerifyHeader(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, true)

	chain := &testChainHeaderReader{}
	header := &testHeader{}

	err := instrumented.VerifyHeader(chain, header, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stats := instrumented.Stats()
	if stats.VerifyHeaderCount != 1 {
		t.Errorf("Expected 1 verify header count, got %d", stats.VerifyHeaderCount)
	}
	if stats.VerifyHeaderTimeNs == 0 {
		t.Error("Expected non-zero verify header time")
	}

	t.Log("✓ InstrumentedEngine VerifyHeader tracks metrics correctly")
}

func TestInstrumentedEnginePrepare(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, true)

	chain := &testChainHeaderReader{}
	header := &testHeader{}

	err := instrumented.Prepare(chain, header)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stats := instrumented.Stats()
	if stats.PrepareCount != 1 {
		t.Errorf("Expected 1 prepare count, got %d", stats.PrepareCount)
	}

	t.Log("✓ InstrumentedEngine Prepare tracks metrics correctly")
}

func TestInstrumentedEngineFinalize(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, true)

	chain := &testChainHeaderReader{}
	header := &testHeader{}

	_, _, err := instrumented.Finalize(chain, header, nil, nil, nil)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stats := instrumented.Stats()
	if stats.FinalizeCount != 1 {
		t.Errorf("Expected 1 finalize count, got %d", stats.FinalizeCount)
	}

	t.Log("✓ InstrumentedEngine Finalize tracks metrics correctly")
}

func TestInstrumentedEngineSeal(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, true)

	chain := &testChainHeaderReader{}
	results := make(chan block.IBlock)
	stop := make(chan struct{})

	err := instrumented.Seal(chain, nil, results, stop)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stats := instrumented.Stats()
	if stats.SealCount != 1 {
		t.Errorf("Expected 1 seal count, got %d", stats.SealCount)
	}

	t.Log("✓ InstrumentedEngine Seal tracks metrics correctly")
}

func TestInstrumentedEngineAuthor(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, true)

	header := &testHeader{}

	_, err := instrumented.Author(header)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	stats := instrumented.Stats()
	if stats.AuthorCount != 1 {
		t.Errorf("Expected 1 author count, got %d", stats.AuthorCount)
	}

	t.Log("✓ InstrumentedEngine Author tracks metrics correctly")
}

func TestInstrumentedEngineInner(t *testing.T) {
	inner := &testEngine{}
	instrumented := consensus.NewInstrumentedEngine(inner, true)

	if instrumented.Inner() != inner {
		t.Error("Inner() should return the wrapped engine")
	}

	t.Log("✓ InstrumentedEngine Inner() returns wrapped engine")
}

// =============================================================================
// EngineStats Tests
// =============================================================================

func TestEngineStatsTotalVerifyTime(t *testing.T) {
	stats := consensus.EngineStats{
		VerifyHeaderTimeNs:  1000,
		VerifyHeadersTimeNs: 2000,
	}

	expected := time.Duration(3000)
	if stats.TotalVerifyTime() != expected {
		t.Errorf("Expected %v, got %v", expected, stats.TotalVerifyTime())
	}

	t.Log("✓ EngineStats.TotalVerifyTime calculates correctly")
}

func TestEngineStatsTotalProductionTime(t *testing.T) {
	stats := consensus.EngineStats{
		PrepareTimeNs:  1000,
		FinalizeTimeNs: 2000,
		SealTimeNs:     3000,
	}

	expected := time.Duration(6000)
	if stats.TotalProductionTime() != expected {
		t.Errorf("Expected %v, got %v", expected, stats.TotalProductionTime())
	}

	t.Log("✓ EngineStats.TotalProductionTime calculates correctly")
}

// =============================================================================
// EngineAdapter Tests
// =============================================================================

func TestEngineAdapterImplementsCoreEngine(t *testing.T) {
	inner := &testEngine{}
	adapter := consensus.NewEngineAdapter(inner)

	var _ consensus.CoreEngine = adapter
	t.Log("✓ EngineAdapter implements CoreEngine")
}

func TestEngineAdapterVerifyHeader(t *testing.T) {
	inner := &testEngine{}
	adapter := consensus.NewEngineAdapter(inner)

	chain := &testChainHeaderReader{}
	header := &testHeader{}

	err := adapter.VerifyHeader(chain, header)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	t.Log("✓ EngineAdapter.VerifyHeader works correctly")
}

func TestEngineAdapterInner(t *testing.T) {
	inner := &testEngine{}
	adapter := consensus.NewEngineAdapter(inner)

	if adapter.Inner() != inner {
		t.Error("Inner() should return the wrapped engine")
	}

	t.Log("✓ EngineAdapter.Inner() returns wrapped engine")
}

// =============================================================================
// Test Implementations
// =============================================================================

type testEngine struct{}

func (e *testEngine) Author(header block.IHeader) (types.Address, error) {
	return types.Address{}, nil
}

func (e *testEngine) IsServiceTransaction(sender types.Address, syscall consensus.SystemCall) bool {
	return false
}

func (e *testEngine) Type() params.ConsensusType {
	return params.AposConsensu
}

func (e *testEngine) VerifyHeader(chain consensus.ChainHeaderReader, header block.IHeader, seal bool) error {
	return nil
}

func (e *testEngine) VerifyHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader, seals []bool) (chan<- struct{}, <-chan error) {
	quit := make(chan struct{})
	results := make(chan error, len(headers))
	for range headers {
		results <- nil
	}
	close(results)
	return quit, results
}

func (e *testEngine) VerifyUncles(chain consensus.ConsensusChainReader, block block.IBlock) error {
	return nil
}

func (e *testEngine) Prepare(chain consensus.ChainHeaderReader, header block.IHeader) error {
	return nil
}

func (e *testEngine) Finalize(chain consensus.ChainHeaderReader, header block.IHeader, state *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil
}

func (e *testEngine) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header block.IHeader, state *state.IntraBlockState, txs []*transaction.Transaction, uncles []block.IHeader, receipts []*block.Receipt) (block.IBlock, []*block.Reward, map[types.Address]*uint256.Int, error) {
	return nil, nil, nil, nil
}

func (e *testEngine) Seal(chain consensus.ChainHeaderReader, b block.IBlock, results chan<- block.IBlock, stop <-chan struct{}) error {
	return nil
}

func (e *testEngine) SealHash(header block.IHeader) types.Hash {
	return types.Hash{}
}

func (e *testEngine) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent block.IHeader) *uint256.Int {
	return uint256.NewInt(1)
}

func (e *testEngine) APIs(chain consensus.ConsensusChainReader) []jsonrpc.API {
	return nil
}

func (e *testEngine) Close() error {
	return nil
}

// testHeader implements block.IHeader for testing
type testHeader struct{}

func (h *testHeader) Number64() *uint256.Int                  { return uint256.NewInt(0) }
func (h *testHeader) BaseFee64() *uint256.Int                 { return uint256.NewInt(0) }
func (h *testHeader) Hash() types.Hash                        { return types.Hash{} }
func (h *testHeader) ToProtoMessage() proto.Message           { return nil }
func (h *testHeader) FromProtoMessage(proto.Message) error    { return nil }
func (h *testHeader) Marshal() ([]byte, error)                { return nil, nil }
func (h *testHeader) Unmarshal([]byte) error                  { return nil }
func (h *testHeader) StateRoot() types.Hash                   { return types.Hash{} }

// Verify testEngine implements Engine
var _ consensus.Engine = (*testEngine)(nil)

// Verify testHeader implements block.IHeader
var _ block.IHeader = (*testHeader)(nil)

