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
//
// EIP-8037 two-dimensional gas metering (Amsterdam).
//
// A frame carries its execution gas in Contract.Gas as before and, under
// Glamsterdam only, a StateGasBudget next to it: the state-gas reservoir
// (the part of tx.gas above TX_MAX_GAS_LIMIT), the net state gas the frame
// charged, and how much of that was borrowed ("spilled") from execution gas
// because the reservoir ran dry. The arithmetic mirrors go-ethereum's
// core/vm/gascosts.go GasBudget.
//
// The reservoir moves between frames whole: a CALL/CREATE hands the
// caller's entire reservoir to the child and takes back whatever the child
// leaves, through EVM.stateGasIn / EVM.stateGasOut so the call signatures
// stay unchanged. On every other chain the budget stays zero and none of
// this runs.

package vm

import "github.com/n42blockchain/N42/params"

// costPerStateByte is the EIP-8037 price of one byte of new state.
const costPerStateByte = params.CostPerStateByteEIP8037

// StateGasBudget is the EIP-8037 state dimension of a frame's gas.
type StateGasBudget struct {
	Reservoir uint64 // remaining state-gas reservoir
	Used      int64  // net state gas charged by this frame (signed: inline refunds)
	Spilled   uint64 // execution gas borrowed to cover state charges
}

// Charge deducts state gas s, spilling into *exec once the reservoir is
// exhausted. It reports false, changing nothing, when exec cannot cover the
// spill.
func (b *StateGasBudget) Charge(exec *uint64, s uint64) bool {
	if s <= b.Reservoir {
		b.Reservoir -= s
	} else {
		spill := s - b.Reservoir
		if spill > *exec {
			return false
		}
		*exec -= spill
		b.Reservoir = 0
		b.Spilled += spill
	}
	b.Used += int64(s)
	return true
}

// CanAfford reports whether execution gas exec plus the reservoir cover an
// execution cost e followed by a state cost s.
func (b *StateGasBudget) CanAfford(exec, e, s uint64) bool {
	if exec < e {
		return false
	}
	if s > b.Reservoir {
		return s-b.Reservoir <= exec-e
	}
	return true
}

// Refund returns s of previously charged state gas: first repaying what was
// borrowed from *exec, the rest back to the reservoir.
func (b *StateGasBudget) Refund(exec *uint64, s uint64) {
	repay := min(s, b.Spilled)
	*exec += repay
	b.Spilled -= repay
	b.Reservoir += s - repay
	b.Used -= int64(s)
}

// failedExit is the state part a frame hands back when it reverts or halts:
// its state changes are rolled back, so every state charge is refilled into
// the reservoir, the borrowed part included (the caller re-credits Spilled
// to execution gas on a revert and burns it on a halt).
func (b StateGasBudget) failedExit() StateGasBudget {
	reservoir := int64(b.Reservoir) + b.Used - int64(b.Spilled)
	if reservoir < 0 {
		reservoir = 0
	}
	return StateGasBudget{Reservoir: uint64(reservoir)}
}

// Exit returns the execution gas and the state part a frame that ended with
// err hands back to its caller, given its running execution gas.
func (b StateGasBudget) Exit(exec uint64, err error) (uint64, StateGasBudget) {
	switch {
	case err == nil:
		return exec, b
	case err == ErrExecutionReverted:
		return exec + b.Spilled, b.failedExit()
	default:
		return 0, b.failedExit()
	}
}

// Absorb merges a child frame's exit-form state budget into this running
// budget. The child's execution leftover must already be added to *exec. A
// child that refilled a charge this frame funded from execution gas hands it
// back in its reservoir, so outstanding debt is settled from it first.
func (b *StateGasBudget) Absorb(exec *uint64, child StateGasBudget) {
	b.Reservoir = child.Reservoir
	b.Used += child.Used
	b.Spilled += child.Spilled
	d := min(b.Reservoir, b.Spilled)
	*exec += d
	b.Reservoir -= d
	b.Spilled -= d
}

// chargeState deducts state gas from the frame (EIP-8037).
func (c *Contract) chargeState(s uint64) bool {
	return c.State.Charge(&c.Gas, s)
}

// refundState refills previously charged state gas into the frame.
func (c *Contract) refundState(s uint64) {
	c.State.Refund(&c.Gas, s)
}

// ForwardStateGas hands the frame's whole reservoir to the next child frame.
func (evm *EVM) ForwardStateGas(c *Contract) {
	evm.SetStateGasReservoir(c.State.Reservoir)
	c.State.Reservoir = 0
}

// AbsorbStateGas takes back what the last child frame left of the reservoir.
// The child's execution leftover must already be in c.Gas.
func (evm *EVM) AbsorbStateGas(c *Contract) {
	evm.stateGasIn = 0
	c.State.Absorb(&c.Gas, evm.stateGasOut)
}

// SetStateGasReservoir sets the reservoir the next frame entered through
// Call/Create starts with. The leftover reads back the same reservoir until
// a frame runs, so a call that fails before entering one returns it intact.
func (evm *EVM) SetStateGasReservoir(r uint64) {
	evm.stateGasIn = r
	evm.stateGasOut = StateGasBudget{Reservoir: r}
}

// StateGasLeftover returns the exit-form state budget of the last frame
// entered through Call/Create.
func (evm *EVM) StateGasLeftover() StateGasBudget {
	return evm.stateGasOut
}

// takeStateGasIn consumes the reservoir handed to the frame being entered.
func (evm *EVM) takeStateGasIn() uint64 {
	r := evm.stateGasIn
	evm.stateGasIn = 0
	evm.stateGasOut = StateGasBudget{Reservoir: r}
	return r
}
