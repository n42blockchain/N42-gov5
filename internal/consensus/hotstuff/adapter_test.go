// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto/bls"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/params"
)

// mockChainHeaderReader implements consensus.ChainHeaderReader for testing.
type mockChainHeaderReader struct {
	headers map[uint64]block.IHeader
}

func newMockChainReader() *mockChainHeaderReader {
	return &mockChainHeaderReader{
		headers: make(map[uint64]block.IHeader),
	}
}

func (m *mockChainHeaderReader) Config() *params.ChainConfig {
	return &params.ChainConfig{}
}

func (m *mockChainHeaderReader) CurrentBlock() block.IBlock {
	return nil
}

func (m *mockChainHeaderReader) GetHeader(_ types.Hash, _ *uint256.Int) block.IHeader {
	return nil
}

func (m *mockChainHeaderReader) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	if number == nil {
		return nil
	}
	return m.headers[number.Uint64()]
}

func (m *mockChainHeaderReader) GetHeaderByHash(_ types.Hash) (block.IHeader, error) {
	return nil, nil
}

func (m *mockChainHeaderReader) GetTd(_ types.Hash, _ *uint256.Int) *uint256.Int {
	return nil
}

func (m *mockChainHeaderReader) GetBlockByNumber(_ *uint256.Int) (block.IBlock, error) {
	return nil, nil
}

func (m *mockChainHeaderReader) GetDepositInfo(_ types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}

func (m *mockChainHeaderReader) GetAccountRewardUnpaid(_ types.Address) (*uint256.Int, error) {
	return nil, nil
}

// Ensure mockChainHeaderReader satisfies the interface.
var _ consensus.ChainHeaderReader = (*mockChainHeaderReader)(nil)

// generateValidatorConfigs creates n validator configs with real BLS keys.
// Returns the configs, secret keys, and addresses.
func generateValidatorConfigs(t *testing.T, n int) ([]params.HotStuffValidatorConfig, []types.Address, *testSetup) {
	t.Helper()
	setup := newTestSetup(t, n)
	configs := make([]params.HotStuffValidatorConfig, n)
	addrs := make([]types.Address, n)
	for i := 0; i < n; i++ {
		addrs[i] = setup.validators[i].Address
		configs[i] = params.HotStuffValidatorConfig{
			Address: setup.validators[i].Address.Hex(),
			BLSKey:  hex.EncodeToString(setup.pubKeys[i].Marshal()),
		}
	}
	return configs, addrs, setup
}

// ============================================================================
// InitEngineFromConfig tests
// ============================================================================

func TestInitEngineFromConfig_Success(t *testing.T) {
	configs, addrs, setup := generateValidatorConfigs(t, 4)

	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Validators:  configs,
	}

	h := New(cfg, nil)
	h.Authorize(addrs[0], setup.keys[0])

	err := h.InitEngineFromConfig()
	if err != nil {
		t.Fatalf("InitEngineFromConfig failed: %v", err)
	}
	if h.Engine() == nil {
		t.Fatal("engine should not be nil after successful initialization")
	}
	if h.Engine().ValidatorCount() != 4 {
		t.Fatalf("expected 4 validators, got %d", h.Engine().ValidatorCount())
	}
}

func TestInitEngineFromConfig_NoValidators(t *testing.T) {
	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Validators:  nil,
	}

	h := New(cfg, nil)
	sk, _ := bls.RandKey()
	h.Authorize(types.Address{1}, sk)

	err := h.InitEngineFromConfig()
	if err == nil {
		t.Fatal("expected error for empty validator list")
	}
}

func TestInitEngineFromConfig_InvalidBLSKey(t *testing.T) {
	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Validators: []params.HotStuffValidatorConfig{
			{
				Address: types.Address{1}.Hex(),
				BLSKey:  "not-valid-hex!!",
			},
		},
	}

	h := New(cfg, nil)
	sk, _ := bls.RandKey()
	h.Authorize(types.Address{1}, sk)

	err := h.InitEngineFromConfig()
	if err == nil {
		t.Fatal("expected error for invalid BLS key hex")
	}
}

func TestInitEngineFromConfig_InvalidBLSKeyBytes(t *testing.T) {
	// Valid hex but not a valid BLS public key.
	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Validators: []params.HotStuffValidatorConfig{
			{
				Address: types.Address{1}.Hex(),
				BLSKey:  hex.EncodeToString([]byte{0x01, 0x02, 0x03}),
			},
		},
	}

	h := New(cfg, nil)
	sk, _ := bls.RandKey()
	h.Authorize(types.Address{1}, sk)

	err := h.InitEngineFromConfig()
	if err == nil {
		t.Fatal("expected error for invalid BLS public key bytes")
	}
}

func TestInitEngineFromConfig_SignerNotInSet(t *testing.T) {
	configs, _, setup := generateValidatorConfigs(t, 4)

	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Validators:  configs,
	}

	// Authorize with an address not in the validator set.
	h := New(cfg, nil)
	outsiderAddr := types.Address{0xFF}
	h.Authorize(outsiderAddr, setup.keys[0])

	err := h.InitEngineFromConfig()
	if err == nil {
		t.Fatal("expected error when signer is not in validator set")
	}
}

func TestInitEngineFromConfig_NotAuthorized(t *testing.T) {
	configs, _, _ := generateValidatorConfigs(t, 4)

	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Validators:  configs,
	}

	h := New(cfg, nil)
	// Do NOT call Authorize.

	err := h.InitEngineFromConfig()
	if err == nil {
		t.Fatal("expected error when Authorize was not called")
	}
}

// ============================================================================
// VerifyHeader tests
// ============================================================================

func TestVerifyHeader_ValidQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Period:      3,
	}
	h := New(cfg, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}

	// Build a valid QC.
	blockHash := types.Hash{0xAB}
	vc := NewVoteCollector(1, blockHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		msg := SigningMessage(1, blockHash)
		sig := setup.keys[i].Sign(msg)
		if err := vc.AddVote(ValidatorIndex(i), sig); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}
	qcData, err := encodeQC(qc)
	if err != nil {
		t.Fatal(err)
	}

	// Build extra-data: magic + view + QC.
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	binary.LittleEndian.PutUint64(extra[extraMagicLen:], 1)
	extra = append(extra, qcData...)

	// Parent header at block 0.
	parentHeader := &block.Header{
		Number: uint256.NewInt(0),
		Time:   1000,
	}
	// Child header at block 1.
	childHeader := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1003,
		Extra:  extra,
	}

	chain := newMockChainReader()
	chain.headers[0] = parentHeader

	err = h.VerifyHeader(chain, childHeader, true)
	if err != nil {
		t.Fatalf("VerifyHeader with valid QC should pass: %v", err)
	}
}

func TestVerifyHeader_InvalidQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	cfg := &params.HotStuffConfig{
		BaseTimeout: 60000,
		MaxTimeout:  120000,
		Period:      3,
	}
	h := New(cfg, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}

	// Build extra-data with corrupted QC data.
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	binary.LittleEndian.PutUint64(extra[extraMagicLen:], 1)
	// Append garbage as QC data.
	extra = append(extra, []byte{0xFF, 0xFE, 0xFD, 0xFC, 0xFB}...)

	parentHeader := &block.Header{
		Number: uint256.NewInt(0),
		Time:   1000,
	}
	childHeader := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1003,
		Extra:  extra,
	}

	chain := newMockChainReader()
	chain.headers[0] = parentHeader

	err := h.VerifyHeader(chain, childHeader, true)
	if err == nil {
		t.Fatal("VerifyHeader with corrupted QC data should fail")
	}
}

func TestVerifyHeader_GenesisBlock(t *testing.T) {
	h := New(nil, nil)

	header := &block.Header{
		Number: uint256.NewInt(0),
	}

	chain := newMockChainReader()
	err := h.VerifyHeader(chain, header, true)
	if err != nil {
		t.Fatalf("genesis block should always pass: %v", err)
	}
}

func TestVerifyHeader_ShortExtra(t *testing.T) {
	h := New(nil, nil)

	parentHeader := &block.Header{
		Number: uint256.NewInt(0),
		Time:   1000,
	}
	childHeader := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1003,
		Extra:  []byte{0x01, 0x02}, // Too short
	}

	chain := newMockChainReader()
	chain.headers[0] = parentHeader

	err := h.VerifyHeader(chain, childHeader, true)
	if err == nil {
		t.Fatal("VerifyHeader with short extra-data should fail")
	}
}

func TestVerifyHeader_NilNumber(t *testing.T) {
	h := New(nil, nil)
	header := &block.Header{
		Number: nil,
	}
	chain := newMockChainReader()
	err := h.VerifyHeader(chain, header, true)
	if err == nil {
		t.Fatal("VerifyHeader with nil number should fail")
	}
}

func TestVerifyHeader_BadMagic(t *testing.T) {
	h := New(nil, nil)

	parentHeader := &block.Header{
		Number: uint256.NewInt(0),
		Time:   1000,
	}
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], []byte("ZZZZ"))
	childHeader := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1003,
		Extra:  extra,
	}

	chain := newMockChainReader()
	chain.headers[0] = parentHeader

	err := h.VerifyHeader(chain, childHeader, true)
	if err == nil {
		t.Fatal("VerifyHeader with bad magic should fail")
	}
}

func TestVerifyHeader_TimestampNotAfterParent(t *testing.T) {
	h := New(nil, nil)

	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])

	parentHeader := &block.Header{
		Number: uint256.NewInt(0),
		Time:   1000,
	}
	childHeader := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1000, // Same as parent, not after
		Extra:  extra,
	}

	chain := newMockChainReader()
	chain.headers[0] = parentHeader

	err := h.VerifyHeader(chain, childHeader, true)
	if err == nil {
		t.Fatal("VerifyHeader with timestamp not after parent should fail")
	}
}
