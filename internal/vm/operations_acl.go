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
// Access-list aware gas calculations for EIP-2929/EIP-2930.
// IsStandardPrecompile identifies addresses 0x01-0x09, which are always
// warm. makeGasSStoreFunc returns a gasFunc that honours the SSTORE
// reentrancy sentry (SstoreSentryGasEIP2200) and then charges
// ColdSloadCostEIP2929 or warm cost based on whether the slot is in
// the transaction access list. Similar helpers back SLOAD, CALL*,
// EXTCODECOPY and friends under the access-list rules.

package vm

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// IsStandardPrecompile checks if an address is a standard precompile (0x01-0x09).
// Per EIP-2929: "The addresses of precompiles from 1 to 9 are always considered warm"
func IsStandardPrecompile(addr types.Address) bool {
	// Check if address is 0x00...01 through 0x00...09
	// All bytes except the last must be zero
	for i := 0; i < 19; i++ {
		if addr[i] != 0 {
			return false
		}
	}
	// Last byte must be 1-9
	return addr[19] >= 1 && addr[19] <= 9
}

// coldAccountAccessCost returns COLD_ACCOUNT_ACCESS for the active rules:
// EIP-8038 (Amsterdam) raises it from 2600 to 3000.
func coldAccountAccessCost(evm VMInterpreter) uint64 {
	if evm.ChainRules().IsGlamsterdam {
		return params.ColdAccountAccessEIP8038
	}
	return params.ColdAccountAccessCostEIP2929
}

func makeGasSStoreFunc(clearingRefund uint64) gasFunc {
	return func(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
		// If we fail the minimum gas availability invariant, fail (0)
		if contract.Gas <= params.SstoreSentryGasEIP2200 {
			return 0, errors.New("not enough gas for reentrancy sentry")
		}
		// Gas sentry honoured, do the actual gas calculation based on the stored value
		var (
			y, x    = stack.Back(1), stack.Peek()
			slot    = types.Hash(x.Bytes32())
			current uint256.Int
			cost    = uint64(0)
		)
		evm.IntraBlockState().GetState(contract.Address(), &slot, &current)
		// Check slot presence in the access list
		if addrPresent, slotPresent := evm.IntraBlockState().SlotInAccessList(contract.Address(), slot); !slotPresent {
			cost = params.ColdSloadCostEIP2929
			// If the caller cannot afford the cost, this change will be rolled back
			evm.IntraBlockState().AddSlotToAccessList(contract.Address(), slot)
			if !addrPresent {
				// This should never happen if the EVM is working correctly
				return 0, errors.New("impossible case: address was not present in access list during sstore op")
			}
		}
		var value uint256.Int
		value.Set(y)

		if current.Eq(&value) { // noop (1)
			// EIP 2200 original clause:
			//		return params.SloadGasEIP2200, nil
			return cost + params.WarmStorageReadCostEIP2929, nil // SLOAD_GAS
		}
		var original uint256.Int
		evm.IntraBlockState().GetCommittedState(contract.Address(), &slot, &original)
		if original.Eq(&current) {
			if original.IsZero() { // create slot (2.1.1)
				return cost + params.SstoreSetGasEIP2200, nil
			}
			if value.IsZero() { // delete slot (2.1.2b)
				evm.IntraBlockState().AddRefund(clearingRefund)
			}
			// EIP-2200 original clause:
			//		return params.SstoreResetGasEIP2200, nil // write existing slot (2.1.2)
			return cost + (params.SstoreResetGasEIP2200 - params.ColdSloadCostEIP2929), nil // write existing slot (2.1.2)
		}
		if !original.IsZero() {
			if current.IsZero() { // recreate slot (2.2.1.1)
				evm.IntraBlockState().SubRefund(clearingRefund)
			} else if value.IsZero() { // delete slot (2.2.1.2)
				evm.IntraBlockState().AddRefund(clearingRefund)
			}
		}
		if original.Eq(&value) {
			if original.IsZero() { // reset to original inexistent slot (2.2.2.1)
				// EIP 2200 Original clause:
				//evm.StateDB.AddRefund(params.SstoreSetGasEIP2200 - params.SloadGasEIP2200)
				evm.IntraBlockState().AddRefund(params.SstoreSetGasEIP2200 - params.WarmStorageReadCostEIP2929)
			} else { // reset to original existing slot (2.2.2.2)
				// EIP 2200 Original clause:
				//	evm.StateDB.AddRefund(params.SstoreResetGasEIP2200 - params.SloadGasEIP2200)
				// - SSTORE_RESET_GAS redefined as (5000 - COLD_SLOAD_COST)
				// - SLOAD_GAS redefined as WARM_STORAGE_READ_COST
				// Final: (5000 - COLD_SLOAD_COST) - WARM_STORAGE_READ_COST
				evm.IntraBlockState().AddRefund((params.SstoreResetGasEIP2200 - params.ColdSloadCostEIP2929) - params.WarmStorageReadCostEIP2929)
			}
		}
		// EIP-2200 original clause:
		//return params.SloadGasEIP2200, nil // dirty update (2.2)
		return cost + params.WarmStorageReadCostEIP2929, nil // dirty update (2.2)
	}
}

// gasSLoadEIP2929 calculates dynamic gas for SLOAD according to EIP-2929
// For SLOAD, if the (address, storage_key) pair (where address is the address of the contract
// whose storage is being read) is not yet in accessed_storage_keys,
// charge 2100 gas and add the pair to accessed_storage_keys.
// If the pair is already in accessed_storage_keys, charge 100 gas.
func gasSLoadEIP2929(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	loc := stack.Peek()
	slot := types.Hash(loc.Bytes32())
	// Check slot presence in the access list
	if _, slotPresent := evm.IntraBlockState().SlotInAccessList(contract.Address(), slot); !slotPresent {
		// If the caller cannot afford the cost, this change will be rolled back
		// If he does afford it, we can skip checking the same thing later on, during execution
		evm.IntraBlockState().AddSlotToAccessList(contract.Address(), slot)
		return params.ColdSloadCostEIP2929, nil
	}
	return params.WarmStorageReadCostEIP2929, nil
}

// gasExtCodeCopyEIP2929 implements extcodecopy according to EIP-2929
// EIP spec:
// > If the target is not in accessed_addresses,
// > charge COLD_ACCOUNT_ACCESS_COST gas, and add the address to accessed_addresses.
// > Otherwise, charge WARM_STORAGE_READ_COST gas.
func gasExtCodeCopyEIP2929(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	// memory expansion first (dynamic part of pre-2929 implementation)
	gas, err := gasExtCodeCopy(evm, contract, stack, mem, memorySize)
	if err != nil {
		return 0, err
	}
	addr := types.Address(stack.Peek().Bytes20())

	// Per EIP-2929: precompiles 1-9 are always warm
	if IsStandardPrecompile(addr) {
		return gas, nil
	}

	// Check slot presence in the access list
	if !evm.IntraBlockState().AddressInAccessList(addr) {
		evm.IntraBlockState().AddAddressToAccessList(addr)
		var overflow bool
		// We charge (cold-warm), since 'warm' is already charged as constantGas
		if gas, overflow = math.SafeAdd(gas, coldAccountAccessCost(evm)-params.WarmStorageReadCostEIP2929); overflow {
			return 0, ErrGasUintOverflow
		}
		return gas, nil
	}
	return gas, nil
}

// gasEip2929AccountCheck checks whether the first stack item (as address) is present in the access list.
// If it is, this method returns '0', otherwise 'cold-warm' gas, presuming that the opcode using it
// is also using 'warm' as constant factor.
// This method is used by:
// - extcodehash,
// - extcodesize,
// - (ext) balance
func gasEip2929AccountCheck(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	addr := types.Address(stack.Peek().Bytes20())

	// Per EIP-2929: precompiles 1-9 are always warm
	if IsStandardPrecompile(addr) {
		return 0, nil
	}

	// Check slot presence in the access list
	if !evm.IntraBlockState().AddressInAccessList(addr) {
		// If the caller cannot afford the cost, this change will be rolled back
		evm.IntraBlockState().AddAddressToAccessList(addr)
		// The warm storage read cost is already charged as constantGas
		return coldAccountAccessCost(evm) - params.WarmStorageReadCostEIP2929, nil
	}
	return 0, nil
}

func delegationCallAccessCost(evm VMInterpreter, addr types.Address) uint64 {
	if !evm.ChainRules().IsPrague {
		return 0
	}

	code := evm.IntraBlockState().GetCode(addr)
	delegatedAddr, ok := ParseDelegation(code)
	if !ok {
		return 0
	}

	if evm.IntraBlockState().AddressInAccessList(delegatedAddr) {
		return params.WarmStorageReadCostEIP2929
	}

	evm.IntraBlockState().AddAddressToAccessList(delegatedAddr)
	return coldAccountAccessCost(evm)
}

func makeCallVariantGasCallEIP2929(oldCalculator gasFunc) gasFunc {
	return func(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
		addr := types.Address(stack.Back(1).Bytes20())

		// Check slot presence in the access list
		// Note: Standard precompiles (0x01-0x09) are added to access list at transaction start
		// via PrepareAccessList, so they will already be warm.
		warmAccess := evm.IntraBlockState().AddressInAccessList(addr)
		// The WarmStorageReadCostEIP2929 (100) is already deducted in the form of a constant cost, so
		// the cost to charge for cold access, if any, is Cold - Warm
		coldCost := coldAccountAccessCost(evm) - params.WarmStorageReadCostEIP2929
		if !warmAccess {
			evm.IntraBlockState().AddAddressToAccessList(addr)
			// Charge the remaining difference here already, to correctly calculate available
			// gas for call
			if !contract.UseGas(coldCost) {
				return 0, ErrOutOfGas
			}
		}

		delegationCost := delegationCallAccessCost(evm, addr)
		if delegationCost > 0 {
			if !contract.UseGas(delegationCost) {
				return 0, ErrOutOfGas
			}
		}
		// Now call the old calculator, which takes into account
		// - create new account
		// - transfer value
		// - memory expansion
		// - 63/64ths rule
		gas, err := oldCalculator(evm, contract, stack, mem, memorySize)
		if err != nil {
			return gas, err
		}
		// In case of a cold access, we temporarily add the cold charge back, and also
		// add it to the returned gas. By adding it to the return, it will be charged
		// outside of this function, as part of the dynamic gas, and that will make it
		// also become correctly reported to tracers.
		if !warmAccess {
			contract.Gas += coldCost
			gas += coldCost
		}
		if delegationCost > 0 {
			contract.Gas += delegationCost
			gas += delegationCost
		}
		return gas, nil
	}
}

var (
	gasCallEIP2929         = makeCallVariantGasCallEIP2929(gasCall)
	gasDelegateCallEIP2929 = makeCallVariantGasCallEIP2929(gasDelegateCall)
	gasStaticCallEIP2929   = makeCallVariantGasCallEIP2929(gasStaticCall)
	gasCallCodeEIP2929     = makeCallVariantGasCallEIP2929(gasCallCode)
	gasSelfdestructEIP2929 = makeSelfdestructGasFn(true)
	// gasSelfdestructEIP3529 implements the changes in EIP-3529 (no refunds)
	gasSelfdestructEIP3529 = makeSelfdestructGasFn(false)

	// gasSStoreEIP2929 implements gas cost for SSTORE according to EIP-2929
	//
	// When calling SSTORE, check if the (address, storage_key) pair is in accessed_storage_keys.
	// If it is not, charge an additional COLD_SLOAD_COST gas, and add the pair to accessed_storage_keys.
	// Additionally, modify the parameters defined in EIP 2200 as follows:
	//
	// Parameter 	Old value 	New value
	// SLOAD_GAS 	800 	= WARM_STORAGE_READ_COST
	// SSTORE_RESET_GAS 	5000 	5000 - COLD_SLOAD_COST
	//
	//The other parameters defined in EIP 2200 are unchanged.
	// see gasSStoreEIP2200(...) in core/vm/gas_table.go for more info about how EIP 2200 is specified
	gasSStoreEIP2929 = makeGasSStoreFunc(params.SstoreClearsScheduleRefundEIP2200)

	// gasSStoreEIP3529 implements gas cost for SSTORE according to EIP-3529
	// Replace `SSTORE_CLEARS_SCHEDULE` with `SSTORE_RESET_GAS + ACCESS_LIST_STORAGE_KEY_COST` (4,800)
	gasSStoreEIP3529 = makeGasSStoreFunc(params.SstoreClearsScheduleRefundEIP3529)

	// gasSStoreGlamsterdam implements gas cost for SSTORE according to EIP-7904 (Glamsterdam)
	// Uses the Glamsterdam clearing refund: SSTORE_RESET_GAS - COLD_SLOAD_COST + ACCESS_LIST_STORAGE_KEY_COST_GLAMSTERDAM (3,375)
	gasSStoreGlamsterdam = makeGasSStoreFunc(params.SstoreClearsScheduleRefundGlamsterdam)
)

// makeSelfdestructGasFn creates the selfdestruct dynamic gas function for EIP-2929 and EIP-3529
func makeSelfdestructGasFn(refundsEnabled bool) gasFunc {
	return func(evm VMInterpreter, contract *Contract, stack *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
		var (
			gas     uint64
			address = types.Address(stack.Peek().Bytes20())
		)
		if !evm.IntraBlockState().AddressInAccessList(address) {
			evm.IntraBlockState().AddAddressToAccessList(address)
			gas = coldAccountAccessCost(evm)
		}
		if evm.IntraBlockState().Empty(address) && !evm.IntraBlockState().GetBalance(contract.Address()).IsZero() {
			newAccountGas := uint64(params.CreateBySelfdestructGas)
			if evm.ChainRules().IsGlamsterdam {
				// EIP-8037/8038: sweeping into a fresh account creates state.
				newAccountGas = params.CreateAccessEIP8038 +
					params.StateBytesPerNewAccount*params.CostPerStateByteEIP8037
			}
			gas += newAccountGas
		}
		if refundsEnabled && !evm.IntraBlockState().HasSelfdestructed(contract.Address()) {
			evm.IntraBlockState().AddRefund(params.SelfdestructRefundGas)
		}
		return gas, nil
	}
}
