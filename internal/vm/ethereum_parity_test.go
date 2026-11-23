package vm

import (
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/params"
)

// parityHarnessConfig is an N42-native fork schedule (Pectra, Osaka, EOF and
// Fusaka from genesis) with the given parity and Glamsterdam times.
func parityHarnessConfig(parity, glamsterdam *big.Int) *params.ChainConfig {
	cfg := execHarnessChainConfig()
	cfg.PectraTime = big.NewInt(0)
	cfg.OsakaTime = big.NewInt(0)
	cfg.EOFTime = big.NewInt(0)
	cfg.FusakaTime = big.NewInt(0)
	cfg.EthereumParityTime = parity
	cfg.GlamsterdamTime = glamsterdam
	return cfg
}

// SLOTNUM under the parity time is what it is on Ethereum: invalid before
// Amsterdam, a push from it. Without the parity time the Fusaka table keeps
// its early SLOTNUM.
func TestEthereumParitySlotNum(t *testing.T) {
	code := []byte{byte(PUSH1), 7, byte(SLOTNUM), byte(STOP)}
	cases := []struct {
		name                string
		parity, glamsterdam *big.Int
		invalid             bool
	}{
		{"fusaka, no parity", nil, nil, false},
		{"fusaka, parity", big.NewInt(0), nil, true},
		{"glamsterdam, parity", big.NewInt(0), big.NewInt(0), false},
	}
	for _, c := range cases {
		evm, ibs := newExecHarnessEVM(t, parityHarnessConfig(c.parity, c.glamsterdam))
		_, _, err := execHarnessCall(t, evm, ibs, code, nil, 100_000, nil)
		var invalid *ErrInvalidOpCode
		if got := errors.As(err, &invalid); got != c.invalid || (err != nil && !got) {
			t.Errorf("%s: err = %v, want invalid opcode = %v", c.name, err, c.invalid)
		}
	}
}

// Both table selections agree under the parity time: no early SLOTNUM and no
// EOF opcodes, although EOFTime and FusakaTime are past.
func TestEthereumParityTables(t *testing.T) {
	cfg := parityHarnessConfig(big.NewInt(0), nil)
	evm, _ := newExecHarnessEVM(t, cfg)
	live := NewEVMInterpreter(evm, Config{}).jt
	cached := newJumpTableForRules(cfg.RulesWithTimestamp(1, 1))
	for name, jt := range map[string]*JumpTable{"interpreter": live, "cache": &cached} {
		for _, op := range []OpCode{SLOTNUM, RJUMP, CALLF, EOFCREATE} {
			if !isUndefinedOp(jt[op]) {
				t.Errorf("%s table: %s is defined under the parity time", name, op)
			}
		}
		if isUndefinedOp(jt[CLZ]) {
			t.Errorf("%s table: CLZ (Osaka) is missing", name)
		}
	}
	without := parityHarnessConfig(nil, nil).RulesWithTimestamp(1, 1)
	if jumpTableCacheKey(without) == jumpTableCacheKey(cfg.RulesWithTimestamp(1, 1)) {
		t.Error("the jump table cache key does not tell the parity rules apart")
	}
}
