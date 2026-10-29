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

package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"reflect"
	"runtime"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func funcName(f interface{}) string {
	v := reflect.ValueOf(f)
	if v.IsNil() {
		return "-"
	}
	return runtime.FuncForPC(v.Pointer()).Name()
}

func opString(op *operation) string {
	return fmt.Sprintf("%s|%d|%s|%d|%d|%d|%t|%t|%t|%d|%s",
		funcName(op.execute), op.constantGas, funcName(op.dynamicGas), op.maxStack,
		op.numPop, op.numPush, op.isPush, op.isSwap, op.isDup, op.opNum, funcName(op.memorySize))
}

// jumpTableFingerprint hashes every opcode's behaviour (handler names, gas,
// stack bounds) so a table's content can be pinned across refactors.
func jumpTableFingerprint(jt JumpTable) string {
	h := sha256.New()
	for i, op := range jt {
		fmt.Fprintf(h, "%02x:%s\n", i, opString(op))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestFusakaTableAddsOnlySlotnum pins what the N42-private Fusaka table adds
// on top of Osaka: SLOTNUM (EIP-7843) and nothing else.
func TestFusakaTableAddsOnlySlotnum(t *testing.T) {
	osaka, fusaka := newOsakaInstructionSet(), newFusakaInstructionSet()
	for i := range osaka {
		if OpCode(i) == SLOTNUM {
			continue
		}
		if a, b := opString(osaka[i]), opString(fusaka[i]); a != b {
			t.Errorf("opcode %v differs: osaka %s, fusaka %s", OpCode(i), a, b)
		}
	}
	if opString(osaka[SLOTNUM]) == opString(fusaka[SLOTNUM]) {
		t.Error("Fusaka table should define SLOTNUM")
	}
}

// TestGlamsterdamTablesPinned pins the Glamsterdam tables, so building them
// on Osaka instead of the N42-private Fusaka table changes no opcode.
func TestGlamsterdamTablesPinned(t *testing.T) {
	for _, tc := range []struct {
		name string
		jt   JumpTable
		want string
	}{
		{"glamsterdam", newGlamsterdamInstructionSet(), "2fb1e9b936251bcad2c65759db337dd55379aeb1aaf5451216d1dad6033c283e"},
		{"glamsterdam-eof", newGlamsterdamEOFInstructionSet(), "af264a0cbc70e50891899d20df57c7473a0ec7f1392522ea9fcaffec3d472d26"},
	} {
		if got := jumpTableFingerprint(tc.jt); got != tc.want {
			t.Errorf("%s fingerprint %s, want %s", tc.name, got, tc.want)
		}
	}
}

// TestEthereumGlamsterdamRulesSelection checks what an Ethereum chain with
// Amsterdam scheduled (and no N42-private Fusaka) executes with: the
// Glamsterdam table and the Osaka precompile set, P-256 included.
func TestEthereumGlamsterdamRulesSelection(t *testing.T) {
	cfg := &params.ChainConfig{
		ChainID:         big.NewInt(1),
		ShanghaiTime:    big.NewInt(0),
		CancunTime:      big.NewInt(0),
		PragueTime:      big.NewInt(0),
		OsakaTime:       big.NewInt(0),
		GlamsterdamTime: big.NewInt(0),
	}
	rules := cfg.RulesWithTimestamp(0, 0)
	if rules.IsFusaka || !rules.IsOsaka || !rules.IsGlamsterdam {
		t.Fatalf("unexpected rules: fusaka=%v osaka=%v glamsterdam=%v", rules.IsFusaka, rules.IsOsaka, rules.IsGlamsterdam)
	}
	if got, want := jumpTableFingerprint(newJumpTableForRules(rules)), jumpTableFingerprint(newGlamsterdamInstructionSet()); got != want {
		t.Fatal("Ethereum Glamsterdam rules did not select the Glamsterdam table")
	}
	if set := activeLegacyPrecompileSet(rules); set != legacyPrecompileSetOsaka {
		t.Fatalf("precompile set %q, want osaka", set)
	}
	if _, ok := legacyPrecompileContractsBySet(legacyPrecompileSetOsaka)[types.HexToAddress("0x0100")]; !ok {
		t.Fatal("Osaka precompile set must include P-256 (EIP-7951)")
	}
}
