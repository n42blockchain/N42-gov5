package txgen

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/runtime"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestERC20Bytecode deploys the hand-assembled ERC20 and exercises the full
// transfer/balanceOf/revert surface through the real EVM.
func TestERC20Bytecode(t *testing.T) {
	deployer := types.HexToAddress("0x00000000000000000000000000000000000000AA")
	bob := types.HexToAddress("0x00000000000000000000000000000000000000BB")

	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	cfg := &runtime.Config{GasLimit: 10_000_000, Origin: deployer, State: ibs}

	_, addr, _, err := runtime.Create(erc20DeployCode(), cfg, 1)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	supply := uint256.MustFromDecimal("1000000000000000000000000000")

	balance := func(a types.Address) *uint256.Int {
		ret, _, err := runtime.Call(addr, erc20BalanceOfCalldata(a), cfg)
		if err != nil {
			t.Fatalf("balanceOf(%s): %v", a.Hex(), err)
		}
		if len(ret) != 32 {
			t.Fatalf("balanceOf returned %d bytes", len(ret))
		}
		v := new(uint256.Int)
		v.SetBytes(ret)
		return v
	}

	if got := balance(deployer); !got.Eq(supply) {
		t.Fatalf("deployer balance after deploy = %s, want %s", got, supply)
	}

	// transfer(bob, 12345) from deployer (Origin).
	amt := uint256.NewInt(12345)
	ret, _, err := runtime.Call(addr, erc20TransferCalldata(bob, amt), cfg)
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if len(ret) != 32 || ret[31] != 1 {
		t.Fatalf("transfer returned %x, want ...01", ret)
	}

	if got := balance(bob); !got.Eq(amt) {
		t.Fatalf("bob balance = %s, want %s", got, amt)
	}
	want := new(uint256.Int).Sub(supply, amt)
	if got := balance(deployer); !got.Eq(want) {
		t.Fatalf("deployer balance = %s, want %s", got, want)
	}

	// Overspend from an empty account must revert: bob has 12345, sends more.
	cfgBob := &runtime.Config{GasLimit: 10_000_000, Origin: bob, State: cfg.State}
	if _, _, err := runtime.Call(addr, erc20TransferCalldata(deployer, uint256.NewInt(99999)), cfgBob); err == nil {
		t.Fatal("overspending transfer did not revert")
	}

	// bob can send within balance.
	if _, _, err := runtime.Call(addr, erc20TransferCalldata(deployer, uint256.NewInt(45)), cfgBob); err != nil {
		t.Fatalf("bob transfer: %v", err)
	}
	if got := balance(bob); !got.Eq(uint256.NewInt(12300)) {
		t.Fatalf("bob balance after send-back = %s, want 12300", got)
	}
}
