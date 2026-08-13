package vm

import (
	"testing"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// TestGuestIntrinsicGas_Transfer verifies intrinsic gas for a simple transfer.
func TestGuestIntrinsicGas_Transfer(t *testing.T) {
	rules := &params.Rules{IsIstanbul: true}
	gas := guestIntrinsicGas(nil, nil, false, rules, false, false)
	if gas != params.TxGas {
		t.Fatalf("expected %d for simple transfer, got %d", params.TxGas, gas)
	}
}

// TestGuestIntrinsicGas_Create verifies intrinsic gas for contract creation.
func TestGuestIntrinsicGas_Create(t *testing.T) {
	rules := &params.Rules{IsIstanbul: true}
	gas := guestIntrinsicGas(nil, nil, true, rules, false, false)
	if gas != params.TxGasContractCreation {
		t.Fatalf("expected %d for create, got %d", params.TxGasContractCreation, gas)
	}
}

// TestGuestIntrinsicGas_DataCost verifies gas calculation for tx data.
func TestGuestIntrinsicGas_DataCost(t *testing.T) {
	// Data with 3 zero bytes and 2 non-zero bytes.
	data := []byte{0x00, 0x01, 0x00, 0x02, 0x00}

	// Pre-Istanbul: non-zero = 68 gas.
	preIstanbul := &params.Rules{IsIstanbul: false}
	gas := guestIntrinsicGas(data, nil, false, preIstanbul, false, false)
	expected := params.TxGas + 2*uint64(params.TxDataNonZeroGasFrontier) + 3*params.TxDataZeroGas
	if gas != expected {
		t.Fatalf("pre-Istanbul data cost: got %d, want %d", gas, expected)
	}

	// Post-Istanbul: non-zero = 16 gas (EIP-2028).
	postIstanbul := &params.Rules{IsIstanbul: true}
	gas = guestIntrinsicGas(data, nil, false, postIstanbul, false, false)
	expected = params.TxGas + 2*params.TxDataNonZeroGasEIP2028 + 3*params.TxDataZeroGas
	if gas != expected {
		t.Fatalf("post-Istanbul data cost: got %d, want %d", gas, expected)
	}
}

// TestGuestIntrinsicGas_AccessList verifies access list gas cost.
func TestGuestIntrinsicGas_AccessList(t *testing.T) {
	rules := &params.Rules{IsBerlin: true, IsIstanbul: true}

	accessList := transaction.AccessList{
		{
			Address: types.Address{0x01},
			StorageKeys: []types.Hash{
				{0x01},
				{0x02},
			},
		},
		{
			Address:     types.Address{0x02},
			StorageKeys: nil,
		},
	}

	gas := guestIntrinsicGas(nil, accessList, false, rules, false, false)
	expected := params.TxGas +
		2*params.TxAccessListAddressGas +
		2*params.TxAccessListStorageKeyGas
	if gas != expected {
		t.Fatalf("access list gas: got %d, want %d", gas, expected)
	}
}

// TestGuestIntrinsicGas_CreateShanghai verifies initcode cost (EIP-3860).
func TestGuestIntrinsicGas_CreateShanghai(t *testing.T) {
	rules := &params.Rules{IsShanghai: true, IsIstanbul: true}

	// 64 bytes of initcode = 2 words.
	initcode := make([]byte, 64)

	gas := guestIntrinsicGas(initcode, nil, true, rules, false, false)
	// Base create + 64 zero bytes + 2 initcode words.
	expected := params.TxGasContractCreation +
		64*params.TxDataZeroGas +
		2*params.InitCodeWordGas
	if gas != expected {
		t.Fatalf("Shanghai initcode gas: got %d, want %d", gas, expected)
	}
}

// TestGuestIntrinsicGas_CreateShanghaiOddSize verifies initcode word rounding.
func TestGuestIntrinsicGas_CreateShanghaiOddSize(t *testing.T) {
	rules := &params.Rules{IsShanghai: true, IsIstanbul: true}

	// 33 bytes = ceil(33/32) = 2 words.
	initcode := make([]byte, 33)

	gas := guestIntrinsicGas(initcode, nil, true, rules, false, false)
	expected := params.TxGasContractCreation +
		33*params.TxDataZeroGas +
		2*params.InitCodeWordGas // ceil(33/32) = 2
	if gas != expected {
		t.Fatalf("odd initcode gas: got %d, want %d", gas, expected)
	}
}

// TestGuestIntrinsicGas_EmptyData verifies no data cost for empty data.
func TestGuestIntrinsicGas_EmptyData(t *testing.T) {
	rules := &params.Rules{IsIstanbul: true}
	gas := guestIntrinsicGas([]byte{}, nil, false, rules, false, false)
	if gas != params.TxGas {
		t.Fatalf("expected %d for empty data, got %d", params.TxGas, gas)
	}
}

// TestGuestIntrinsicGas_AllNonZero verifies cost with all non-zero data bytes.
func TestGuestIntrinsicGas_AllNonZero(t *testing.T) {
	rules := &params.Rules{IsIstanbul: true}
	data := []byte{0xFF, 0xFE, 0xFD}
	gas := guestIntrinsicGas(data, nil, false, rules, false, false)
	expected := params.TxGas + 3*params.TxDataNonZeroGasEIP2028
	if gas != expected {
		t.Fatalf("all non-zero data cost: got %d, want %d", gas, expected)
	}
}

// TestGuestIntrinsicGas_Combined verifies combined gas calculation
// (create + data + access list + initcode).
func TestGuestIntrinsicGas_Combined(t *testing.T) {
	rules := &params.Rules{
		IsIstanbul: true,
		IsBerlin:   true,
		IsShanghai: true,
	}

	data := []byte{0x00, 0x01} // 1 zero + 1 non-zero
	accessList := transaction.AccessList{
		{Address: types.Address{0x01}, StorageKeys: []types.Hash{{0x01}}},
	}

	gas := guestIntrinsicGas(data, accessList, true, rules, false, false)
	expected := params.TxGasContractCreation +
		1*params.TxDataZeroGas +
		1*params.TxDataNonZeroGasEIP2028 +
		1*params.TxAccessListAddressGas +
		1*params.TxAccessListStorageKeyGas +
		1*params.InitCodeWordGas // ceil(2/32) = 1 word
	if gas != expected {
		t.Fatalf("combined gas: got %d, want %d", gas, expected)
	}
}

// TestApplyMessageForGuest_InvalidEVM verifies rejection of non-EVM types.
func TestApplyMessageForGuest_InvalidEVM(t *testing.T) {
	// InstrumentedVM is a VMInterface but not *EVM.
	fakeEVM := &InstrumentedVM{}
	gp := new(common.GasPool).AddGas(1000000)

	_, err := ApplyMessageForGuest(fakeEVM, transaction.Message{}, gp)
	if err != errGuestInvalidEVM {
		t.Fatalf("expected errGuestInvalidEVM, got %v", err)
	}
}

// TestApplyMessageForGuest_NilEVM verifies nil EVM panics are avoided.
func TestApplyMessageForGuest_NilEVM(t *testing.T) {
	gp := new(common.GasPool).AddGas(1000000)

	_, err := ApplyMessageForGuest(nil, transaction.Message{}, gp)
	if err != errGuestInvalidEVM {
		t.Fatalf("expected errGuestInvalidEVM for nil EVM, got %v", err)
	}
}
