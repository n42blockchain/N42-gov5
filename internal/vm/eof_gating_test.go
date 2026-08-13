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

// EOF activation gating. EOF was removed from the Fusaka (Osaka EL) scope
// upstream and geth rejects every 0xEF deployment on mainnet, so a
// mainnet-following chain must never activate EOF via Osaka — only via the
// explicit EOFTime experimental gate. These tests pin that separation.

package vm

import (
	"testing"

	"github.com/n42blockchain/N42/params"
)

// TestMainnetRulesDoNotActivateEOF pins the divergence-critical invariant:
// the Ethereum mainnet config (which drives eth-el mainnet following) has
// IsOsaka active since Fusaka but must never report IsEOF.
func TestMainnetRulesDoNotActivateEOF(t *testing.T) {
	cfg := params.EthereumMainnetChainConfig
	fusakaMainnet := uint64(1764798551) // Osaka/Fusaka EL activation on mainnet
	if !cfg.IsOsaka(fusakaMainnet) {
		t.Fatal("mainnet IsOsaka must be active at the Fusaka timestamp")
	}
	if cfg.IsEOF(fusakaMainnet) || cfg.IsEOF(^uint64(0)>>1) {
		t.Fatal("mainnet must NEVER activate EOF (geth rejects all 0xEF deployments)")
	}
}

// TestOsakaTableHasNoEOFWithoutGate: the plain Osaka+ tables must not carry
// EOF opcodes; the EOF variants must.
func TestOsakaTableHasNoEOFWithoutGate(t *testing.T) {
	for _, op := range []OpCode{RJUMP, RJUMPI, CALLF, RETF, EOFCREATE, DATALOAD} {
		if osakaInstructionSet[op] != nil && osakaInstructionSet[op].execute != nil {
			// A defined operation here would let legacy mainnet code execute
			// EOF opcodes that geth treats as invalid.
			if opCodeToString[op] != "" && !isUndefinedOp(osakaInstructionSet[op]) {
				t.Fatalf("opcode %s active in plain Osaka table — mainnet divergence", op)
			}
		}
		if osakaEOFInstructionSet[op] == nil || osakaEOFInstructionSet[op].execute == nil {
			t.Fatalf("opcode %s missing from the EOF-variant Osaka table", op)
		}
	}
}

// isUndefinedOp reports whether the operation slot is the shared "undefined"
// placeholder (jump tables may fill empty slots with an invalid-op handler).
func isUndefinedOp(op *operation) bool {
	return op.numPop == 0 && op.numPush == 0 && op.constantGas == 0 && op.dynamicGas == nil
}
