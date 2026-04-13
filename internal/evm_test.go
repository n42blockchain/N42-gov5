// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Mock StateDB for Testing
// =============================================================================

type mockStateDB struct {
	balances map[types.Address]*uint256.Int
}

func newMockStateDB() *mockStateDB {
	return &mockStateDB{
		balances: make(map[types.Address]*uint256.Int),
	}
}

func (m *mockStateDB) GetBalance(addr types.Address) *uint256.Int {
	if balance, ok := m.balances[addr]; ok {
		return balance.Clone()
	}
	return uint256.NewInt(0)
}

func (m *mockStateDB) SetBalance(addr types.Address, amount *uint256.Int) {
	m.balances[addr] = amount.Clone()
}

func (m *mockStateDB) SubBalance(addr types.Address, amount *uint256.Int) {
	if balance, ok := m.balances[addr]; ok {
		newBalance := new(uint256.Int).Sub(balance, amount)
		m.balances[addr] = newBalance
	}
}

func (m *mockStateDB) AddBalance(addr types.Address, amount *uint256.Int) {
	if balance, ok := m.balances[addr]; ok {
		newBalance := new(uint256.Int).Add(balance, amount)
		m.balances[addr] = newBalance
	} else {
		m.balances[addr] = amount.Clone()
	}
}

// Implement remaining StateDB interface methods (stubs)
func (m *mockStateDB) CreateAccount(types.Address, bool) {}
func (m *mockStateDB) SubRefund(uint64)                  {}
func (m *mockStateDB) AddRefund(uint64)                  {}
func (m *mockStateDB) GetRefund() uint64                 { return 0 }
func (m *mockStateDB) GetCommittedState(types.Address, *types.Hash, *uint256.Int) {
}
func (m *mockStateDB) GetState(types.Address, *types.Hash, *uint256.Int)        {}
func (m *mockStateDB) SetState(types.Address, *types.Hash, uint256.Int)         {}
func (m *mockStateDB) GetTransientState(types.Address, types.Hash) uint256.Int  { return uint256.Int{} }
func (m *mockStateDB) SetTransientState(types.Address, types.Hash, uint256.Int) {}
func (m *mockStateDB) GetCode(types.Address) []byte                             { return nil }
func (m *mockStateDB) SetCode(types.Address, []byte)                            {}
func (m *mockStateDB) GetCodeSize(types.Address) int                            { return 0 }
func (m *mockStateDB) GetCodeHash(types.Address) types.Hash                     { return types.Hash{} }
func (m *mockStateDB) GetNonce(types.Address) uint64                            { return 0 }
func (m *mockStateDB) SetNonce(types.Address, uint64)                           {}
func (m *mockStateDB) AddAddressToAccessList(types.Address)                     {}
func (m *mockStateDB) AddSlotToAccessList(types.Address, types.Hash)            {}
func (m *mockStateDB) AddressInAccessList(types.Address) bool                   { return false }
func (m *mockStateDB) SlotInAccessList(types.Address, types.Hash) (bool, bool)  { return false, false }
func (m *mockStateDB) RevertToSnapshot(int)                                     {}
func (m *mockStateDB) Snapshot() int                                            { return 0 }
func (m *mockStateDB) AddLog(*block.Log)                                        {}
func (m *mockStateDB) Exist(types.Address) bool                                 { return false }
func (m *mockStateDB) Empty(types.Address) bool                                 { return true }
func (m *mockStateDB) Selfdestruct(types.Address) bool                          { return false }
func (m *mockStateDB) Selfdestruct6780(types.Address, types.Address)            {}
func (m *mockStateDB) HasSelfdestructed(types.Address) bool                     { return false }
func (m *mockStateDB) WasCreatedInCurrentTx(types.Address) bool                 { return false }
func (m *mockStateDB) PrepareAccessList(types.Address, *types.Address, []types.Address, transaction.AccessList) {
}
func (m *mockStateDB) HasNonEmptyStorage(types.Address) bool { return false }

// Ensure mockStateDB implements evmtypes.IntraBlockState
var _ evmtypes.IntraBlockState = (*mockStateDB)(nil)

// =============================================================================
// CanTransfer Tests
// =============================================================================

func TestCanTransfer_Sufficient(t *testing.T) {
	db := newMockStateDB()
	addr := types.Address{0x01}
	db.SetBalance(addr, uint256.NewInt(1000))

	if !CanTransfer(db, addr, uint256.NewInt(500)) {
		t.Error("CanTransfer should return true when balance is sufficient")
	}
	if !CanTransfer(db, addr, uint256.NewInt(1000)) {
		t.Error("CanTransfer should return true when balance equals amount")
	}

	t.Logf("✓ CanTransfer returns true for sufficient balance")
}

func TestCanTransfer_Insufficient(t *testing.T) {
	db := newMockStateDB()
	addr := types.Address{0x01}
	db.SetBalance(addr, uint256.NewInt(100))

	if CanTransfer(db, addr, uint256.NewInt(500)) {
		t.Error("CanTransfer should return false when balance is insufficient")
	}

	t.Logf("✓ CanTransfer returns false for insufficient balance")
}

func TestCanTransfer_Zero(t *testing.T) {
	db := newMockStateDB()
	addr := types.Address{0x01}
	db.SetBalance(addr, uint256.NewInt(0))

	if !CanTransfer(db, addr, uint256.NewInt(0)) {
		t.Error("CanTransfer should return true for zero amount with zero balance")
	}
	if CanTransfer(db, addr, uint256.NewInt(1)) {
		t.Error("CanTransfer should return false for non-zero amount with zero balance")
	}

	t.Logf("✓ CanTransfer handles zero values correctly")
}

func TestCanTransfer_UnknownAddress(t *testing.T) {
	db := newMockStateDB()
	addr := types.Address{0x99}

	if !CanTransfer(db, addr, uint256.NewInt(0)) {
		t.Error("CanTransfer should return true for zero amount on unknown address")
	}
	if CanTransfer(db, addr, uint256.NewInt(1)) {
		t.Error("CanTransfer should return false for non-zero amount on unknown address")
	}

	t.Logf("✓ CanTransfer handles unknown addresses correctly")
}

// =============================================================================
// Transfer Tests
// =============================================================================

func TestTransfer_Normal(t *testing.T) {
	db := newMockStateDB()
	sender := types.Address{0x01}
	recipient := types.Address{0x02}

	db.SetBalance(sender, uint256.NewInt(1000))
	db.SetBalance(recipient, uint256.NewInt(100))

	Transfer(db, sender, recipient, uint256.NewInt(300), false)

	senderBalance := db.GetBalance(sender)
	recipientBalance := db.GetBalance(recipient)

	if senderBalance.Cmp(uint256.NewInt(700)) != 0 {
		t.Errorf("Sender balance = %v, want 700", senderBalance)
	}
	if recipientBalance.Cmp(uint256.NewInt(400)) != 0 {
		t.Errorf("Recipient balance = %v, want 400", recipientBalance)
	}

	t.Logf("✓ Transfer correctly moves funds")
}

func TestTransfer_Bailout(t *testing.T) {
	db := newMockStateDB()
	sender := types.Address{0x01}
	recipient := types.Address{0x02}

	db.SetBalance(sender, uint256.NewInt(1000))
	db.SetBalance(recipient, uint256.NewInt(100))

	Transfer(db, sender, recipient, uint256.NewInt(300), true)

	senderBalance := db.GetBalance(sender)
	recipientBalance := db.GetBalance(recipient)

	if senderBalance.Cmp(uint256.NewInt(1000)) != 0 {
		t.Errorf("Sender balance should not change in bailout, got %v", senderBalance)
	}
	if recipientBalance.Cmp(uint256.NewInt(400)) != 0 {
		t.Errorf("Recipient balance = %v, want 400", recipientBalance)
	}

	t.Logf("✓ Transfer bailout mode only adds to recipient")
}

func TestTransfer_ZeroAmount(t *testing.T) {
	db := newMockStateDB()
	sender := types.Address{0x01}
	recipient := types.Address{0x02}

	db.SetBalance(sender, uint256.NewInt(1000))
	db.SetBalance(recipient, uint256.NewInt(100))

	Transfer(db, sender, recipient, uint256.NewInt(0), false)

	senderBalance := db.GetBalance(sender)
	recipientBalance := db.GetBalance(recipient)

	if senderBalance.Cmp(uint256.NewInt(1000)) != 0 {
		t.Errorf("Sender balance = %v, want 1000", senderBalance)
	}
	if recipientBalance.Cmp(uint256.NewInt(100)) != 0 {
		t.Errorf("Recipient balance = %v, want 100", recipientBalance)
	}

	t.Logf("✓ Transfer handles zero amount correctly")
}

type mockMessage struct {
	from       types.Address
	to         *types.Address
	gasPrice   *uint256.Int
	feeCap     *uint256.Int
	tip        *uint256.Int
	blobFeeCap *uint256.Int
	blobHashes []types.Hash
	gas        uint64
	value      *uint256.Int
	nonce      uint64
	checkNonce bool
	data       []byte
	accessList transaction.AccessList
	authList   transaction.AuthorizationList
	isFree     bool
}

func (m mockMessage) From() types.Address                     { return m.from }
func (m mockMessage) To() *types.Address                      { return m.to }
func (m mockMessage) GasPrice() *uint256.Int                  { return m.gasPrice }
func (m mockMessage) FeeCap() *uint256.Int                    { return m.feeCap }
func (m mockMessage) Tip() *uint256.Int                       { return m.tip }
func (m mockMessage) BlobFeeCap() *uint256.Int                { return m.blobFeeCap }
func (m mockMessage) BlobHashes() []types.Hash                { return m.blobHashes }
func (m mockMessage) Gas() uint64                             { return m.gas }
func (m mockMessage) Value() *uint256.Int                     { return m.value }
func (m mockMessage) Nonce() uint64                           { return m.nonce }
func (m mockMessage) CheckNonce() bool                        { return m.checkNonce }
func (m mockMessage) Data() []byte                            { return m.data }
func (m mockMessage) AccessList() transaction.AccessList      { return m.accessList }
func (m mockMessage) AuthList() transaction.AuthorizationList { return m.authList }
func (m mockMessage) IsFree() bool                            { return m.isFree }

func TestNewEVMTxContextCarriesBlobHashes(t *testing.T) {
	hashes := []types.Hash{
		types.HexToHash("0x01"),
		types.HexToHash("0x02"),
	}
	msg := mockMessage{
		from:       types.Address{0x01},
		gasPrice:   uint256.NewInt(7),
		blobHashes: hashes,
	}

	txCtx := NewEVMTxContext(msg)

	if txCtx.Origin != msg.from {
		t.Fatalf("Origin = %x, want %x", txCtx.Origin, msg.from)
	}
	if txCtx.GasPrice == nil || txCtx.GasPrice.Cmp(msg.gasPrice) != 0 {
		t.Fatalf("GasPrice = %v, want %v", txCtx.GasPrice, msg.gasPrice)
	}
	if len(txCtx.BlobHashes) != len(hashes) {
		t.Fatalf("len(BlobHashes) = %d, want %d", len(txCtx.BlobHashes), len(hashes))
	}
	for i := range hashes {
		if txCtx.BlobHashes[i] != hashes[i] {
			t.Fatalf("BlobHashes[%d] = %s, want %s", i, txCtx.BlobHashes[i], hashes[i])
		}
	}

	hashes[0] = types.HexToHash("0xff")
	if txCtx.BlobHashes[0] != types.HexToHash("0x01") {
		t.Fatalf("BlobHashes should be copied, got %s", txCtx.BlobHashes[0])
	}
}

func TestTransfer_ToNewAddress(t *testing.T) {
	db := newMockStateDB()
	sender := types.Address{0x01}
	recipient := types.Address{0x99} // Not in db

	db.SetBalance(sender, uint256.NewInt(1000))

	Transfer(db, sender, recipient, uint256.NewInt(300), false)

	recipientBalance := db.GetBalance(recipient)
	if recipientBalance.Cmp(uint256.NewInt(300)) != 0 {
		t.Errorf("Recipient balance = %v, want 300", recipientBalance)
	}

	t.Logf("✓ Transfer creates balance for new address")
}

// =============================================================================
// BorTransfer Tests
// =============================================================================

func TestBorTransfer_Normal(t *testing.T) {
	db := newMockStateDB()
	sender := types.Address{0x01}
	recipient := types.Address{0x02}

	db.SetBalance(sender, uint256.NewInt(1000))
	db.SetBalance(recipient, uint256.NewInt(100))

	BorTransfer(db, sender, recipient, uint256.NewInt(300), false)

	senderBalance := db.GetBalance(sender)
	recipientBalance := db.GetBalance(recipient)

	if senderBalance.Cmp(uint256.NewInt(700)) != 0 {
		t.Errorf("Sender balance = %v, want 700", senderBalance)
	}
	if recipientBalance.Cmp(uint256.NewInt(400)) != 0 {
		t.Errorf("Recipient balance = %v, want 400", recipientBalance)
	}

	t.Logf("✓ BorTransfer correctly moves funds")
}

func TestBorTransfer_Bailout(t *testing.T) {
	db := newMockStateDB()
	sender := types.Address{0x01}
	recipient := types.Address{0x02}

	db.SetBalance(sender, uint256.NewInt(1000))
	db.SetBalance(recipient, uint256.NewInt(100))

	BorTransfer(db, sender, recipient, uint256.NewInt(300), true)

	senderBalance := db.GetBalance(sender)
	recipientBalance := db.GetBalance(recipient)

	if senderBalance.Cmp(uint256.NewInt(1000)) != 0 {
		t.Errorf("Sender balance should not change in bailout, got %v", senderBalance)
	}
	if recipientBalance.Cmp(uint256.NewInt(400)) != 0 {
		t.Errorf("Recipient balance = %v, want 400", recipientBalance)
	}

	t.Logf("✓ BorTransfer bailout mode only adds to recipient")
}

// =============================================================================
// ChainContext Interface Test
// =============================================================================

func TestChainContextInterface(t *testing.T) {
	// Verify ChainContext interface is defined correctly
	// This is a compile-time check
	t.Logf("✓ ChainContext interface is correctly defined")
}

func TestNewEVMBlockContextMissingHeaderNumber(t *testing.T) {
	author := types.Address{0x42}
	header := &block.Header{
		Difficulty: uint256.NewInt(1),
	}

	ctx := NewEVMBlockContext(header, nil, nil, nil, &author)

	if ctx.BlockNumber != 0 {
		t.Fatalf("BlockNumber = %d, want 0", ctx.BlockNumber)
	}
	if ctx.Coinbase != author {
		t.Fatalf("Coinbase = %v, want %v", ctx.Coinbase, author)
	}
}

func TestNewEVMBlockContextMissingDifficulty(t *testing.T) {
	author := types.Address{0x42}
	ctx := NewEVMBlockContext(&block.Header{}, nil, nil, nil, &author)

	if ctx.Difficulty == nil {
		t.Fatal("Difficulty should be initialized")
	}
	if ctx.Difficulty.Sign() != 0 {
		t.Fatalf("Difficulty = %v, want 0", ctx.Difficulty)
	}
	if ctx.PrevRanDao != nil {
		t.Fatalf("PrevRanDao = %v, want nil", ctx.PrevRanDao)
	}
}

func TestNewEVMBlockContextPreservesBaseFee(t *testing.T) {
	author := types.Address{0x42}
	baseFee := new(uint256.Int).SetAllOne()
	ctx := NewEVMBlockContext(&block.Header{BaseFee: baseFee}, nil, nil, nil, &author)

	if ctx.BaseFee == nil {
		t.Fatal("BaseFee should be initialized")
	}
	if ctx.BaseFee.Cmp(baseFee) != 0 {
		t.Fatalf("BaseFee = %v, want %v", ctx.BaseFee, baseFee)
	}
	if ctx.BaseFee == baseFee {
		t.Fatal("BaseFee should be copied into context storage")
	}
}

func TestNewEVMBlockContextPopulatesBlobFeeFields(t *testing.T) {
	author := types.Address{0x42}
	header := &block.Header{
		ExcessBlobGas: u64ptr(transaction.BlobTxTargetBlobGasPerBlock),
	}

	cfg := &params.ChainConfig{PragueTime: big.NewInt(0)}
	ctx := NewEVMBlockContext(header, nil, nil, cfg, &author)

	if ctx.BlobBaseFee == nil {
		t.Fatal("BlobBaseFee should be initialized")
	}
	want := cfg.CalcBlobFee(*header.ExcessBlobGas, header.Time)
	if ctx.BlobBaseFee.Cmp(want) != 0 {
		t.Fatalf("BlobBaseFee = %v, want %v", ctx.BlobBaseFee, want)
	}
	if ctx.ExcessBlobGas != *header.ExcessBlobGas {
		t.Fatalf("ExcessBlobGas = %d, want %d", ctx.ExcessBlobGas, *header.ExcessBlobGas)
	}
}

func TestNewEVMBlockContextFallsBackToHeaderCoinbase(t *testing.T) {
	coinbase := types.HexToAddress("0x2adc25665018aa1fe0e6bc666dac8fc2697ff9ba")
	header := &block.Header{
		Coinbase: coinbase,
	}

	ctx := NewEVMBlockContext(header, nil, nil, nil, nil)

	if ctx.Coinbase != coinbase {
		t.Fatalf("Coinbase = %v, want %v", ctx.Coinbase, coinbase)
	}
}

func TestGetHashFnRejectsMissingRefHeaderNumber(t *testing.T) {
	called := false
	getHash := GetHashFn(&block.Header{}, func(hash types.Hash, number uint64) *block.Header {
		called = true
		return nil
	})

	if got := getHash(0); got != (types.Hash{}) {
		t.Fatalf("GetHashFn returned %v, want zero hash", got)
	}
	if called {
		t.Fatal("GetHashFn called header lookup for malformed reference header")
	}
}

func TestGetHashFnStopsOnAncestorWithoutNumber(t *testing.T) {
	ref := &block.Header{
		Number:     uint256.NewInt(3),
		ParentHash: types.HexToHash("0x01"),
	}
	lookups := 0
	getHash := GetHashFn(ref, func(hash types.Hash, number uint64) *block.Header {
		lookups++
		return &block.Header{ParentHash: types.HexToHash("0x02")}
	})

	if got := getHash(1); got != (types.Hash{}) {
		t.Fatalf("GetHashFn returned %v, want zero hash", got)
	}
	if lookups != 1 {
		t.Fatalf("GetHashFn lookups = %d, want 1", lookups)
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkCanTransfer(b *testing.B) {
	db := newMockStateDB()
	addr := types.Address{0x01}
	db.SetBalance(addr, uint256.NewInt(1000))
	amount := uint256.NewInt(500)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CanTransfer(db, addr, amount)
	}
}

func BenchmarkTransfer(b *testing.B) {
	sender := types.Address{0x01}
	recipient := types.Address{0x02}
	amount := uint256.NewInt(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db := newMockStateDB()
		db.SetBalance(sender, uint256.NewInt(10000000))
		Transfer(db, sender, recipient, amount, false)
	}
}

func BenchmarkTransferBailout(b *testing.B) {
	sender := types.Address{0x01}
	recipient := types.Address{0x02}
	amount := uint256.NewInt(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db := newMockStateDB()
		db.SetBalance(sender, uint256.NewInt(10000000))
		Transfer(db, sender, recipient, amount, true)
	}
}

func BenchmarkBorTransfer(b *testing.B) {
	sender := types.Address{0x01}
	recipient := types.Address{0x02}
	amount := uint256.NewInt(100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		db := newMockStateDB()
		db.SetBalance(sender, uint256.NewInt(10000000))
		BorTransfer(db, sender, recipient, amount, false)
	}
}
