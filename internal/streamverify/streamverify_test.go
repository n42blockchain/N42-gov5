// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package streamverify

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// stubReader is a tiny in-memory state.StateReader for testing the
// recorder without spinning up a real DB.
type stubReader struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
	codes    map[types.Hash][]byte
	err      error
}

func (s *stubReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.accounts[addr], nil
}

func (s *stubReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	if m, ok := s.storage[addr]; ok {
		return m[*key], nil
	}
	return nil, nil
}

func (s *stubReader) ReadAccountCode(_ types.Address, codeHash types.Hash) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.codes[codeHash], nil
}

func (s *stubReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	c, err := s.ReadAccountCode(addr, codeHash)
	return len(c), err
}

// ----------------------------------------------------------------------------
// Recorder unit tests
// ----------------------------------------------------------------------------

func TestRecorder_AccountReadCapture(t *testing.T) {
	addr := types.HexToAddress("0x000000000000000000000000000000000000beef")
	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 7
	acc.Balance.SetUint64(123456)
	acc.CodeHash = evmsdk.EmptyCodeHash

	reader := &stubReader{accounts: map[types.Address]*account.StateAccount{addr: &acc}}
	rec := NewReadLogRecorder(reader, nil)

	got, err := rec.ReadAccountData(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Nonce != 7 {
		t.Fatalf("inner read failed: %+v", got)
	}

	log := rec.Log()
	if len(log) != 1 {
		t.Fatalf("log entries = %d, want 1", len(log))
	}
	entry, ok := log[0].(evmsdk.ReadLogAccount)
	if !ok {
		t.Fatalf("entry type = %T, want ReadLogAccount", log[0])
	}
	if entry.Nonce != 7 || entry.Balance.Uint64() != 123456 {
		t.Fatalf("captured nonce/balance wrong: %+v", entry)
	}
}

func TestRecorder_MissingAccountCapture(t *testing.T) {
	rec := NewReadLogRecorder(&stubReader{}, nil)
	got, err := rec.ReadAccountData(types.Address{0x9})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing account, got %+v", got)
	}
	if _, ok := rec.Log()[0].(evmsdk.ReadLogAccountNotFound); !ok {
		t.Fatalf("expected ReadLogAccountNotFound, got %T", rec.Log()[0])
	}
}

func TestRecorder_StorageReadCapture(t *testing.T) {
	addr := types.HexToAddress("0x1234")
	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000007")
	reader := &stubReader{
		storage: map[types.Address]map[types.Hash][]byte{
			addr: {key: []byte{0x42}},
		},
	}
	rec := NewReadLogRecorder(reader, nil)

	v, err := rec.ReadAccountStorage(addr, &key)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 1 || v[0] != 0x42 {
		t.Fatalf("inner storage read wrong: %x", v)
	}

	entry, ok := rec.Log()[0].(evmsdk.ReadLogStorage)
	if !ok {
		t.Fatalf("expected ReadLogStorage, got %T", rec.Log()[0])
	}
	if entry.Value.Uint64() != 0x42 {
		t.Fatalf("captured storage value wrong: %s", entry.Value)
	}
}

func TestRecorder_BytecodeCaptureSkippedForKnown(t *testing.T) {
	codeHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	reader := &stubReader{codes: map[types.Hash][]byte{codeHash: {0x60, 0x00, 0xf3}}}
	known := map[types.Hash]struct{}{codeHash: {}}
	rec := NewReadLogRecorder(reader, known)

	if _, err := rec.ReadAccountCode(types.Address{}, codeHash); err != nil {
		t.Fatal(err)
	}
	if len(rec.UncachedBytecodes()) != 0 {
		t.Fatalf("known code was captured, want skipped: %v", rec.UncachedBytecodes())
	}
}

func TestRecorder_BytecodeCaptureIdempotent(t *testing.T) {
	codeHash := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	reader := &stubReader{codes: map[types.Hash][]byte{codeHash: {0xab, 0xcd}}}
	rec := NewReadLogRecorder(reader, nil)

	// Read the same hash 5 times — should appear once in captured map.
	for i := 0; i < 5; i++ {
		if _, err := rec.ReadAccountCode(types.Address{}, codeHash); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.UncachedBytecodes()) != 1 {
		t.Fatalf("captured count = %d, want 1", len(rec.UncachedBytecodes()))
	}
}

func TestRecorder_BytecodeCaptureSkipsEmpty(t *testing.T) {
	reader := &stubReader{}
	rec := NewReadLogRecorder(reader, nil)
	if _, err := rec.ReadAccountCode(types.Address{}, evmsdk.EmptyCodeHash); err != nil {
		t.Fatal(err)
	}
	if len(rec.UncachedBytecodes()) != 0 {
		t.Fatalf("emptyCodeHash should not be captured")
	}
}

func TestRecorder_Reset(t *testing.T) {
	addr := types.HexToAddress("0x1234")
	codeHash := types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333")
	reader := &stubReader{
		accounts: map[types.Address]*account.StateAccount{addr: nil},
		codes:    map[types.Hash][]byte{codeHash: {0xff}},
	}
	rec := NewReadLogRecorder(reader, nil)

	_, _ = rec.ReadAccountData(addr)
	_, _ = rec.ReadAccountCode(addr, codeHash)
	if len(rec.Log()) == 0 || len(rec.UncachedBytecodes()) == 0 {
		t.Fatal("recorder did not capture")
	}

	rec.Reset()
	if len(rec.Log()) != 0 {
		t.Fatalf("after reset log len = %d, want 0", len(rec.Log()))
	}
	if len(rec.UncachedBytecodes()) != 0 {
		t.Fatalf("after reset bytecodes len = %d, want 0", len(rec.UncachedBytecodes()))
	}
}

func TestRecorder_PropagatesErrors(t *testing.T) {
	wantErr := errors.New("inner storage read failed")
	reader := &stubReader{err: wantErr}
	rec := NewReadLogRecorder(reader, nil)

	if _, err := rec.ReadAccountData(types.Address{}); !errors.Is(err, wantErr) {
		t.Fatalf("ReadAccountData err = %v, want %v", err, wantErr)
	}
}

// ----------------------------------------------------------------------------
// BuildStreamPacket
// ----------------------------------------------------------------------------

// validHeader returns a header with every required field filled in so
// header.Marshal() does not panic on nil pointer fields. Tests that
// don't care about specific values use this as a baseline and override
// only what they need.
func validHeader(t *testing.T, blockNumber uint64, receiptRoot types.Hash) *block.Header {
	t.Helper()
	return &block.Header{
		ParentHash:  types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
		UncleHash:   types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:    types.HexToAddress("0x000000000000000000000000000000000000beef"),
		Root:        types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"),
		TxHash:      types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		ReceiptHash: receiptRoot,
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(blockNumber),
		GasLimit:    30_000_000,
		GasUsed:     0,
		Time:        1700000000,
		Extra:       []byte{},
		BaseFee:     uint256.NewInt(1_000_000_000),
	}
}

func TestBuildStreamPacket_Empty(t *testing.T) {
	hdr := validHeader(t, 1, types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"))
	rec := NewReadLogRecorder(&stubReader{}, nil)

	pkt, err := BuildStreamPacket(hdr, nil, rec)
	if err != nil {
		t.Fatal(err)
	}
	if pkt.BlockHash != hdr.Hash() {
		t.Fatalf("packet block hash != header hash")
	}
	if len(pkt.Transactions) != 0 {
		t.Fatalf("expected 0 txs, got %d", len(pkt.Transactions))
	}
	if len(pkt.Bytecodes) != 0 {
		t.Fatalf("expected 0 bytecodes, got %d", len(pkt.Bytecodes))
	}
}

func TestBuildStreamPacket_NilArgs(t *testing.T) {
	if _, err := BuildStreamPacket(nil, nil, &ReadLogRecorder{}); err == nil {
		t.Fatal("expected nil header error")
	}
	if _, err := BuildStreamPacket(&block.Header{}, nil, nil); err == nil {
		t.Fatal("expected nil recorder error")
	}
}

func TestBuildStreamPacket_BytecodesSorted(t *testing.T) {
	hashes := []types.Hash{
		types.HexToHash("0xff00000000000000000000000000000000000000000000000000000000000000"),
		types.HexToHash("0x1100000000000000000000000000000000000000000000000000000000000000"),
		types.HexToHash("0xaa00000000000000000000000000000000000000000000000000000000000000"),
	}
	codes := map[types.Hash][]byte{
		hashes[0]: {0x60, 0x00},
		hashes[1]: {0x60, 0x01},
		hashes[2]: {0x60, 0x02},
	}
	reader := &stubReader{codes: codes}
	rec := NewReadLogRecorder(reader, nil)
	for _, h := range hashes {
		if _, err := rec.ReadAccountCode(types.Address{}, h); err != nil {
			t.Fatal(err)
		}
	}

	hdr := validHeader(t, 1, types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"))
	pkt, err := BuildStreamPacket(hdr, nil, rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkt.Bytecodes) != 3 {
		t.Fatalf("expected 3 bytecodes, got %d", len(pkt.Bytecodes))
	}
	for i := 1; i < len(pkt.Bytecodes); i++ {
		prev := pkt.Bytecodes[i-1].Hash
		cur := pkt.Bytecodes[i].Hash
		// hash[0] of sorted should ascend.
		if prev[0] > cur[0] {
			t.Fatalf("bytecodes not sorted: %x then %x", prev, cur)
		}
	}
}

// ----------------------------------------------------------------------------
// END-TO-END: producer → verifier roundtrip
// ----------------------------------------------------------------------------
//
// This is the validation criterion for Phase 2: a packet built by
// BuildStreamPacket must round-trip cleanly through
// cmd/evmsdk.ExecuteAndVerifyV2 with no mismatches.
//
// We use the empty-block path (no txs → empty receipts → empty trie root)
// because building a real-tx block requires a full executor stack. The
// real-tx path is exercised by the existing executor + journal_verify
// tests; this test exists to prove the packet wire format is symmetric.
// ----------------------------------------------------------------------------

func TestProducerVerifier_EmptyBlockRoundtrip(t *testing.T) {
	emptyTrieRoot := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	hdr := validHeader(t, 100, emptyTrieRoot)

	// 1. Producer side: empty execution → empty recorder → packet.
	rec := NewReadLogRecorder(&stubReader{}, nil)
	pkt, err := BuildStreamPacket(hdr, nil, rec)
	if err != nil {
		t.Fatalf("BuildStreamPacket: %v", err)
	}

	// 2. Verify packet round-trips through wire format.
	encoded, err := evmsdk.EncodeStreamPacket(pkt)
	if err != nil {
		t.Fatalf("EncodeStreamPacket: %v", err)
	}
	decoded, err := evmsdk.DecodeStreamPacket(encoded)
	if err != nil {
		t.Fatalf("DecodeStreamPacket: %v", err)
	}

	// 3. Verifier side: re-execute the decoded packet.
	codes := evmsdk.NewCodeCache(16)
	res, err := evmsdk.ExecuteAndVerifyV2(decoded, codes, nil)
	if err != nil {
		t.Fatalf("ExecuteAndVerifyV2: %v", err)
	}

	// 4. Assertions.
	if res.BlockHash != hdr.Hash() {
		t.Errorf("verifier block hash != producer header hash")
	}
	if res.BlockNumber != 100 {
		t.Errorf("blockNumber = %d, want 100", res.BlockNumber)
	}
	if res.TxCount != 0 {
		t.Errorf("txCount = %d, want 0", res.TxCount)
	}
	if res.WitnessReadLogLen != 0 {
		t.Errorf("readLog len = %d, want 0", res.WitnessReadLogLen)
	}
	if res.ComputedReceiptsRoot != emptyTrieRoot {
		t.Errorf("receipts root = %x, want empty trie", res.ComputedReceiptsRoot)
	}
}

// TestProducerVerifier_ReadLogRoundtrip exercises the wire format with
// non-empty captured reads. We synthesize a recorder by hand (without
// running the EVM) and assert the packet's encoded read log matches what
// cmd/evmsdk.DecodeReadLog reconstructs.
func TestProducerVerifier_ReadLogRoundtrip(t *testing.T) {
	addr1 := types.HexToAddress("0x1111111111111111111111111111111111111111")
	addr2 := types.HexToAddress("0x2222222222222222222222222222222222222222")
	codeHash := types.HexToHash("0xabcdef0000000000000000000000000000000000000000000000000000000000")
	storageKey := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000007")

	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = 5
	acc.Balance.SetUint64(1_000_000)
	acc.CodeHash = codeHash

	reader := &stubReader{
		accounts: map[types.Address]*account.StateAccount{addr1: &acc},
		storage: map[types.Address]map[types.Hash][]byte{
			addr1: {storageKey: {0x42}},
		},
		codes: map[types.Hash][]byte{codeHash: {0x60, 0x00, 0xf3}},
	}
	rec := NewReadLogRecorder(reader, nil)

	if _, err := rec.ReadAccountData(addr1); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.ReadAccountStorage(addr1, &storageKey); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.ReadAccountCode(addr1, codeHash); err != nil {
		t.Fatal(err)
	}
	if _, err := rec.ReadAccountData(addr2); err != nil { // missing → AccountNotFound
		t.Fatal(err)
	}

	if len(rec.Log()) != 3 {
		t.Fatalf("log len = %d, want 3 (account, storage, missing)", len(rec.Log()))
	}
	if len(rec.UncachedBytecodes()) != 1 {
		t.Fatalf("captured codes = %d, want 1", len(rec.UncachedBytecodes()))
	}

	// Build packet, then decode the read log on the verifier side.
	hdr := validHeader(t, 1, types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"))
	pkt, err := BuildStreamPacket(hdr, nil, rec)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := evmsdk.DecodeReadLog(pkt.ReadLogData)
	if err != nil {
		t.Fatalf("DecodeReadLog: %v", err)
	}
	if len(decoded) != 3 {
		t.Fatalf("decoded entries = %d, want 3", len(decoded))
	}

	a, ok := decoded[0].(evmsdk.ReadLogAccount)
	if !ok || a.Nonce != 5 || a.Balance.Uint64() != 1_000_000 || a.CodeHash != codeHash {
		t.Fatalf("decoded entry 0 wrong: %+v (ok=%v)", a, ok)
	}
	s, ok := decoded[1].(evmsdk.ReadLogStorage)
	if !ok || s.Value.Uint64() != 0x42 {
		t.Fatalf("decoded entry 1 wrong: %+v", decoded[1])
	}
	if _, ok := decoded[2].(evmsdk.ReadLogAccountNotFound); !ok {
		t.Fatalf("decoded entry 2 wrong: %T", decoded[2])
	}

	// Bytecode should be in the packet's Bytecodes section.
	if len(pkt.Bytecodes) != 1 {
		t.Fatalf("packet bytecodes = %d, want 1", len(pkt.Bytecodes))
	}
	if pkt.Bytecodes[0].Hash != codeHash {
		t.Fatalf("packet bytecode hash mismatch")
	}
	if len(pkt.Bytecodes[0].Code) != 3 || pkt.Bytecodes[0].Code[2] != 0xf3 {
		t.Fatalf("packet bytecode body mismatch: %x", pkt.Bytecodes[0].Code)
	}
}
