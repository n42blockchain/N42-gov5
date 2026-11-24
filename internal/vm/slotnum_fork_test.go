package vm

import (
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params"
)

// SLOTNUM is what it is on Ethereum: an invalid opcode before Amsterdam, on
// the N42 Fusaka table too, and a push from Glamsterdam.
func TestSlotNumIsInvalidBeforeGlamsterdam(t *testing.T) {
	code := []byte{byte(PUSH1), 7, byte(SLOTNUM), byte(STOP)}
	for _, glamsterdam := range []*big.Int{nil, big.NewInt(0)} {
		cfg := execHarnessChainConfig()
		cfg.PectraTime = big.NewInt(0)
		cfg.OsakaTime = big.NewInt(0)
		cfg.FusakaTime = big.NewInt(0)
		cfg.GlamsterdamTime = glamsterdam

		evm, ibs := newExecHarnessEVM(t, cfg)
		_, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
		var invalid *ErrInvalidOpCode
		if glamsterdam == nil && !errors.As(err, &invalid) {
			t.Errorf("Fusaka: err = %v, want an invalid opcode", err)
		}
		if glamsterdam != nil && err != nil {
			t.Errorf("Glamsterdam: err = %v, want SLOTNUM to execute", err)
		}
	}
}

// Both table selections give a Fusaka chain a table without SLOTNUM and with
// Osaka's CLZ.
func TestFusakaChainTablesHaveNoSlotNum(t *testing.T) {
	rules := &params.Rules{IsCancun: true, IsPrague: true, IsPectra: true, IsOsaka: true, IsFusaka: true}
	cached := newJumpTableForRules(rules)
	for name, jt := range map[string]*JumpTable{"interpreter": &fusakaInstructionSet, "cache": &cached} {
		if !isUndefinedOp(jt[SLOTNUM]) {
			t.Errorf("%s table: SLOTNUM is defined before Glamsterdam", name)
		}
		if isUndefinedOp(jt[CLZ]) {
			t.Errorf("%s table: CLZ (Osaka) is missing", name)
		}
	}
}
