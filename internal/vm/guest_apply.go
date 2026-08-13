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
// Self-contained message application entry point for the zkVM guest.
// ApplyMessageForGuest reproduces the full transaction state-transition
// fee model - buyGas, EVM call or create, refundGas, coinbase tip
// payment - without importing package internal, so the guest binary
// stays dependency-light. GuestExecutionResult returns UsedGas, Err and
// ReturnData. Errors errGuestInvalidEVM, errGuestIntrinsicGas,
// errGuestNonceMismatch and errGuestBuyGas classify validation failures.

package vm

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/params"
)

var (
	errGuestInvalidEVM    = errors.New("vm: invalid EVM type for guest execution")
	errGuestIntrinsicGas  = errors.New("vm: intrinsic gas too low")
	errGuestNonceMismatch = errors.New("vm: nonce mismatch in guest execution")
	errGuestBuyGas        = errors.New("vm: insufficient funds to buy gas in guest execution")
)

// GuestExecutionResult is the result of applying a message in the guest context.
type GuestExecutionResult struct {
	UsedGas    uint64
	Err        error
	ReturnData []byte
}

// ApplyMessageForGuest executes a message against the EVM for the zkVM guest.
// This is a self-contained entry point that avoids importing package `internal`.
//
// It faithfully reproduces the full state-transition fee model:
//  1. buyGas: deduct gas * gasPrice from sender
//  2. execute EVM call/create
//  3. refundGas: return unused gas * gasPrice to sender
//  4. coinbase payment: effectiveTip * gasUsed to block producer
func ApplyMessageForGuest(evm VMInterface, msg transaction.Message, gp *common.GasPool) (*GuestExecutionResult, error) {
	realEVM, ok := evm.(*EVM)
	if !ok {
		return nil, errGuestInvalidEVM
	}

	sender := msg.From()
	ibs := realEVM.IntraBlockState()
	gas := msg.Gas()
	rules := realEVM.ChainRules()
	gasPrice := msg.GasPrice()

	// Nonce validation: verify the transaction nonce matches the sender's current nonce.
	// Defense-in-depth — the witness state is Merkle-verified, so a tampered
	// nonce would be caught by witness verification. But we check here to fail fast.
	if msg.CheckNonce() {
		stateNonce := ibs.GetNonce(sender)
		if msgNonce := msg.Nonce(); msgNonce != stateNonce {
			return nil, errGuestNonceMismatch
		}
	}

	// Step 1: Buy gas — deduct gas * gasPrice from sender balance.
	mgval := new(uint256.Int).SetUint64(gas)
	mgval.Mul(mgval, gasPrice)
	if have := ibs.GetBalance(sender); have.Cmp(mgval) < 0 {
		return nil, fmt.Errorf("%w: have %v, want %v", errGuestBuyGas, have, mgval)
	}
	ibs.SubBalance(sender, mgval)

	if err := gp.SubGas(gas); err != nil {
		return nil, err
	}

	gasRemaining := gas

	// Compute intrinsic gas.
	guestHasValue := msg.Value() != nil && !msg.Value().IsZero()
	guestSelfTransfer := msg.To() != nil && *msg.To() == msg.From()
	intrinsicGas := guestIntrinsicGas(msg.Data(), msg.AccessList(), msg.To() == nil, rules, guestHasValue, guestSelfTransfer)
	if gasRemaining < intrinsicGas {
		return nil, errGuestIntrinsicGas
	}
	gasRemaining -= intrinsicGas

	// Set up access list (EIP-2929).
	if rules.IsBerlin {
		ibs.PrepareAccessList(sender, msg.To(), ActivePrecompiles(rules), msg.AccessList())
		// EIP-3651: Warm COINBASE
		if rules.IsShanghai {
			ibs.AddAddressToAccessList(realEVM.Context().Coinbase)
		}
	}

	var (
		ret   []byte
		vmerr error
	)

	if msg.To() == nil {
		ret, _, gasRemaining, vmerr = realEVM.Create(AccountRef(sender), msg.Data(), gasRemaining, msg.Value())
	} else {
		ibs.SetNonce(sender, ibs.GetNonce(sender)+1)
		ret, gasRemaining, vmerr = realEVM.Call(AccountRef(sender), *msg.To(), msg.Data(), gasRemaining, msg.Value(), false)
	}

	// Step 3: Refund gas — return unused gas * gasPrice to sender.
	// EIP-3529: max refund = gasUsed / 5 after London (was gasUsed / 2 before).
	gasUsed := gas - gasRemaining
	refund := ibs.GetRefund()
	maxRefund := gasUsed / 2
	if rules.IsLondon {
		maxRefund = gasUsed / 5
	}
	if refund > maxRefund {
		refund = maxRefund
	}
	gasRemaining += refund
	gasUsed = gas - gasRemaining

	// Refund remaining gas value to sender.
	remaining := new(uint256.Int).SetUint64(gasRemaining)
	remaining.Mul(remaining, gasPrice)
	ibs.AddBalance(sender, remaining)

	gp.AddGas(gasRemaining)

	// Step 4: Pay coinbase — effectiveTip * gasUsed to block producer.
	effectiveTip := gasPrice
	if rules.IsLondon && realEVM.Context().BaseFee != nil {
		baseFee := realEVM.Context().BaseFee
		if gasPrice.Gt(baseFee) {
			effectiveTip = new(uint256.Int).Sub(gasPrice, baseFee)
			// Cap at msg tip (gasTipCap) — but in guest context gasPrice is
			// already the effective gas price from AsMessage, so this is correct.
		} else {
			effectiveTip = new(uint256.Int)
		}
	}
	coinbasePayment := new(uint256.Int).SetUint64(gasUsed)
	coinbasePayment.Mul(coinbasePayment, effectiveTip)
	ibs.AddBalance(realEVM.Context().Coinbase, coinbasePayment)

	return &GuestExecutionResult{
		UsedGas:    gasUsed,
		Err:        vmerr,
		ReturnData: ret,
	}, nil
}

// guestIntrinsicGas computes the intrinsic gas cost for a transaction.
// Returns math.MaxUint64 on overflow to ensure the transaction is rejected.
func guestIntrinsicGas(data []byte, accessList transaction.AccessList, isCreate bool, rules *params.Rules, hasValue, isSelfTransfer bool) uint64 {
	var gas uint64
	if rules.IsGlamsterdam {
		// EIP-2780 component-based intrinsic gas (mirrors internal.IntrinsicGas).
		gas = params.TxBaseCostEIP2780
		if isCreate {
			gas += params.CreateAccessEIP8038
		} else if !isSelfTransfer {
			gas += params.TxColdAccountEIP2780
			if hasValue {
				gas += params.TxValueCostEIP2780
			}
		}
	} else if isCreate {
		gas = params.TxGasContractCreation
	} else {
		gas = params.TxGas
	}

	// Data cost.
	if len(data) > 0 {
		var nz uint64
		for _, byt := range data {
			if byt != 0 {
				nz++
			}
		}
		nonZeroGas := uint64(params.TxDataNonZeroGasFrontier)
		if rules.IsGlamsterdam || rules.IsIstanbul {
			// Amsterdam keeps the 4/16 calldata prices (EIP-7976 only raises
			// the EIP-7623 floor).
			nonZeroGas = params.TxDataNonZeroGasEIP2028
		}
		// Overflow check: nz * nonZeroGas.
		if nz > 0 && nonZeroGas > 0 && nz > (^uint64(0))/nonZeroGas {
			return ^uint64(0)
		}
		nzCost := nz * nonZeroGas
		if gas > ^uint64(0)-nzCost {
			return ^uint64(0)
		}
		gas += nzCost

		zeroGas := uint64(params.TxDataZeroGas)
		z := uint64(len(data)) - nz
		if z > 0 && zeroGas > 0 && z > (^uint64(0))/zeroGas {
			return ^uint64(0)
		}
		zCost := z * zeroGas
		if gas > ^uint64(0)-zCost {
			return ^uint64(0)
		}
		gas += zCost
	}

	// Access list cost.
	if len(accessList) > 0 {
		accessListAddressGas := uint64(params.TxAccessListAddressGas)
		accessListStorageKeyGas := uint64(params.TxAccessListStorageKeyGas)
		if rules.IsGlamsterdam {
			// EIP-8038 base prices + EIP-7981 per-byte surcharge.
			accessListAddressGas = params.TxAccessListAddressGasEIP8038 +
				params.AccessListAddressBytes*params.AccessListBytesSurchargeEIP7981
			accessListStorageKeyGas = params.TxAccessListStorageKeyGasEIP8038 +
				params.AccessListStorageKeyBytes*params.AccessListBytesSurchargeEIP7981
		}
		addrCount := uint64(len(accessList))
		if addrCount > (^uint64(0))/accessListAddressGas {
			return ^uint64(0)
		}
		addrCost := addrCount * accessListAddressGas
		if gas > ^uint64(0)-addrCost {
			return ^uint64(0)
		}
		gas += addrCost
		for _, al := range accessList {
			keyCount := uint64(len(al.StorageKeys))
			if keyCount > 0 {
				if keyCount > (^uint64(0))/accessListStorageKeyGas {
					return ^uint64(0)
				}
				keyCost := keyCount * accessListStorageKeyGas
				if gas > ^uint64(0)-keyCost {
					return ^uint64(0)
				}
				gas += keyCost
			}
		}
	}

	// Initcode cost (EIP-3860).
	if isCreate && rules.IsShanghai {
		lenWords := (uint64(len(data)) + 31) / 32
		initCost := lenWords * params.InitCodeWordGas
		if gas > ^uint64(0)-initCost {
			return ^uint64(0)
		}
		gas += initCost
	}

	return gas
}
