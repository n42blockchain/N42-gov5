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
// Block-level gas accounting primitive. GasPool tracks the remaining
// gas budget while executing transactions in a block. AddGas panics on
// overflow, SubGas returns ErrGasLimitReached when the pool is exhausted,
// Gas reads the current level and the zero value is a valid empty pool.
// Under EIP-8037 (Amsterdam) the block is metered in two dimensions,
// execution and state; CheckGasAmsterdam/ChargeGasAmsterdam keep those
// counters and are only used on Glamsterdam blocks.

package common

import (
	"errors"
	"fmt"
	"math"
)

// ErrGasLimitReached is returned by the gas pool if the amount of gas required
// by a transaction is higher than what's left in the block.
var ErrGasLimitReached = errors.New("gas limit reached")

// GasPool tracks the amount of gas available during execution of the transactions
// in a block. The zero value is a pool with zero gas available.
type GasPool struct {
	gas uint64

	// EIP-8037 (Amsterdam): the block gas limit and the cumulative
	// execution and state gas of the transactions charged so far. The
	// block's gas used is max(cumExec, cumState). Set up by the first
	// Amsterdam call, from the pool's level at that point.
	twoD     bool
	initial  uint64
	cumExec  uint64
	cumState uint64
}

// NewGasPool returns a pool holding amount gas.
func NewGasPool(amount uint64) *GasPool {
	return &GasPool{gas: amount}
}

// AddGas makes gas available for execution.
func (gp *GasPool) AddGas(amount uint64) *GasPool {
	if gp.gas > math.MaxUint64-amount {
		panic("gas pool pushed above uint64")
	}
	gp.gas += amount
	return gp
}

// SubGas deducts the given amount from the pool if enough gas is
// available and returns an error otherwise.
func (gp *GasPool) SubGas(amount uint64) error {
	if gp.gas < amount {
		return ErrGasLimitReached
	}
	gp.gas -= amount
	return nil
}

// Gas returns the amount of gas remaining in the pool.
func (gp *GasPool) Gas() uint64 {
	return gp.gas
}

func (gp *GasPool) String() string {
	return fmt.Sprintf("%d", gp.gas)
}

func (gp *GasPool) initTwoD() {
	if !gp.twoD {
		gp.twoD = true
		gp.initial = gp.gas
	}
}

// CheckGasAmsterdam is the EIP-8037 per-transaction inclusion check: the
// worst-case execution contribution must fit what is left of the execution
// dimension and the worst-case state contribution what is left of the state
// dimension. Nothing is deducted.
func (gp *GasPool) CheckGasAmsterdam(execReservation, stateReservation uint64) error {
	gp.initTwoD()
	if gp.initial-gp.cumExec < execReservation || gp.initial-gp.cumState < stateReservation {
		return ErrGasLimitReached
	}
	return nil
}

// ChargeGasAmsterdam adds a transaction's execution and state gas to the
// block counters (EIP-8037), failing when the block's gas used,
// max(execution, state), would exceed its gas limit.
func (gp *GasPool) ChargeGasAmsterdam(txExec, txState uint64) error {
	gp.initTwoD()
	cumExec, cumState := gp.cumExec+txExec, gp.cumState+txState
	if used := max(cumExec, cumState); used > gp.initial {
		return fmt.Errorf("%w: block gas overflow: limit %d, used %d (execution %d, state %d)",
			ErrGasLimitReached, gp.initial, used, cumExec, cumState)
	}
	gp.cumExec, gp.cumState = cumExec, cumState
	gp.gas = gp.initial - cumExec
	return nil
}

// UsedAmsterdam returns the block's gas used under EIP-8037,
// max(cumulative execution gas, cumulative state gas).
func (gp *GasPool) UsedAmsterdam() uint64 {
	return max(gp.cumExec, gp.cumState)
}
