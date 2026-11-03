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
// Amsterdam (Glamsterdam) gas functions and frame-entering opcodes for
// EIP-8037 (state creation gas, two-dimensional metering) and EIP-8038
// (state access gas), ported from go-ethereum's core/vm gas_table.go,
// operations_acl.go and instructions.go. Only the Glamsterdam jump table
// installs them (enable8037And8038); every other fork keeps its own.

package vm

import (
	"errors"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// Amsterdam gas constants (EIP-8037 / EIP-8038).
const (
	coldAccountAccess8038 = params.ColdAccountAccessEIP8038 // 3000
	warmAccess8038        = params.WarmStorageReadCostEIP2929
	accountWrite8038      = params.AccountWriteEIP8038 // 9000
	// callValue8038 is CALL_VALUE = ACCOUNT_WRITE + CALL_STIPEND.
	callValue8038      = params.AccountWriteEIP8038 + params.CallStipend // 11300
	coldStorage8038    = params.ColdSloadCostEIP2929                     // 2100
	storageWrite8038   = params.StorageWriteEIP8038                      // 10000
	storageClear8038   = params.StorageClearRefundEIP8038                // 11616
	newAccountState    = params.StateBytesPerNewAccount * costPerStateByte
	newStorageSlotGas  = params.StateBytesPerStorageSet * costPerStateByte
	delegationStateGas = params.StateBytesPerAuthBase * costPerStateByte
)

// NewAccountStateGas is the EIP-8037 state gas of one new account.
const NewAccountStateGas = newAccountState

// DelegationStateGas is the EIP-8037 state gas of a new EIP-7702 delegation
// designator.
const DelegationStateGas = delegationStateGas

// enable8037And8038 installs the Amsterdam state-access and state-creation
// pricing and the reservoir-aware frame-entering opcodes.
func enable8037And8038(jt *JumpTable) {
	jt[SSTORE].dynamicGas = gasSStore8037
	jt[EXTCODESIZE].dynamicGas = gasExtCodeSize8038
	jt[EXTCODECOPY].dynamicGas = gasExtCodeCopy8038
	jt[EXTCODEHASH].dynamicGas = gasEip2929AccountCheck
	jt[BALANCE].dynamicGas = gasEip2929AccountCheck

	jt[CALL].dynamicGas = gasCall8037
	jt[CALL].execute = opCall8037
	jt[CALLCODE].dynamicGas = gasCallCode8038
	jt[CALLCODE].execute = opCallCode8037
	jt[DELEGATECALL].dynamicGas = gasDelegateCall8038
	jt[DELEGATECALL].execute = opDelegateCall8037
	jt[STATICCALL].dynamicGas = gasStaticCall8038
	jt[STATICCALL].execute = opStaticCall8037

	jt[CREATE].constantGas = params.CreateAccessEIP8038
	jt[CREATE].dynamicGas = gasCreate8037
	jt[CREATE].execute = opCreate8037
	jt[CREATE2].constantGas = params.CreateAccessEIP8038
	jt[CREATE2].dynamicGas = gasCreate2_8037
	jt[CREATE2].execute = opCreate2_8037

	jt[SELFDESTRUCT].dynamicGas = gasSelfdestruct8037
	jt[SELFDESTRUCT].execute = opSelfdestruct8246
}

// gasSStore8037 prices SSTORE under EIP-8037/8038: a cold or warm slot
// access, STORAGE_WRITE on the first change of a slot, and state gas for a
// slot created from zero, refilled inline when the slot is reset to zero.
func gasSStore8037(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	// A static frame is rejected before the slot is read, as go-ethereum
	// does, so the slot stays out of the EIP-7928 block access list.
	if interpreterReadOnly(evm) {
		return 0, ErrWriteProtection
	}
	if contract.Gas <= params.SstoreSentryGasEIP2200 {
		return 0, errors.New("not enough gas for reentrancy sentry")
	}
	var (
		y, x    = stk.Back(1), stk.Peek()
		slot    = &contract.gasSlot
		current = &contract.gasCur
		ibs     = evm.IntraBlockState()
		addr    = contract.Address()
	)
	*slot = types.Hash(x.Bytes32())
	access := warmAccess8038
	_, slotPresent := ibs.SlotInAccessList(addr, *slot)
	if !slotPresent {
		access = coldStorage8038
	}
	// The access must be affordable before the slot is read.
	if contract.Gas < access {
		return 0, errors.New("not enough gas for slot access")
	}
	if !slotPresent {
		ibs.AddSlotToAccessList(addr, *slot)
	}
	ibs.GetState(addr, slot, current)
	var value uint256.Int
	value.Set(y)
	if current.Eq(&value) { // noop
		return access, nil
	}
	original := &contract.gasOrig
	ibs.GetCommittedState(addr, slot, original)
	if original.Eq(current) { // first change of the slot
		if original.IsZero() { // create slot
			if !contract.CanAffordState(access+storageWrite8038, newStorageSlotGas) ||
				!contract.chargeState(newStorageSlotGas) {
				return 0, ErrOutOfGas
			}
			return access + storageWrite8038, nil
		}
		if value.IsZero() { // delete slot
			ibs.AddRefund(storageClear8038)
		}
		return access + storageWrite8038, nil
	}
	if !original.IsZero() {
		if current.IsZero() { // recreate slot
			ibs.SubRefund(storageClear8038)
		} else if value.IsZero() { // delete slot
			ibs.AddRefund(storageClear8038)
		}
	}
	if original.Eq(&value) { // reset to original value
		if original.IsZero() { // reset to original inexistent slot
			contract.refundState(newStorageSlotGas)
		}
		ibs.AddRefund(storageWrite8038)
	}
	return access, nil // dirty update
}

// CanAffordState reports whether the frame covers an execution cost e
// followed by a state cost s.
func (c *Contract) CanAffordState(e, s uint64) bool {
	return c.State.CanAfford(c.Gas, e, s)
}

// gasExtCodeSize8038 is the account access check plus a WARM_ACCESS for the
// second database read (the code size).
func gasExtCodeSize8038(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	gas, err := gasEip2929AccountCheck(evm, contract, stk, mem, memorySize)
	if err != nil {
		return 0, err
	}
	return gas + warmAccess8038, nil
}

// gasExtCodeCopy8038 is the EIP-2929 EXTCODECOPY price at the EIP-8038 cold
// rate plus a WARM_ACCESS for the second database read (the code).
func gasExtCodeCopy8038(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	gas, err := gasExtCodeCopyEIP2929(evm, contract, stk, mem, memorySize)
	if err != nil {
		return 0, err
	}
	var overflow bool
	if gas, overflow = math.SafeAdd(gas, warmAccess8038); overflow {
		return 0, ErrGasUintOverflow
	}
	return gas, nil
}

// callExecutionGasFunc prices the execution part of a CALL-family opcode
// other than account access: memory expansion and the value transfer.
type callExecutionGasFunc func(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error)

func callValueExecutionGas(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	gas, err := memoryGasCost(mem, memorySize)
	if err != nil {
		return 0, err
	}
	if !stk.Back(2).IsZero() {
		var overflow bool
		if gas, overflow = math.SafeAdd(gas, callValue8038); overflow {
			return 0, ErrGasUintOverflow
		}
	}
	return gas, nil
}

func callNoValueExecutionGas(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	return memoryGasCost(mem, memorySize)
}

// makeCallVariantGas8037 prices a CALL-family opcode under Amsterdam, in
// go-ethereum's order: cold account access, the execution cost (the guard
// before any further state access), EIP-7702 delegation resolution, then for
// CALL the state gas of a value transfer into an empty account, and last the
// 63/64 forward over what is left. The direct charges are added back and
// returned so the interpreter charges them as the opcode's dynamic cost; the
// state charge stays applied.
func makeCallVariantGas8037(execFn callExecutionGasFunc, newAccountState bool) gasFunc {
	return func(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
		var (
			ibs         = evm.IntraBlockState()
			addr        = types.Address(stk.Back(1).Bytes20())
			accessCost  uint64
			delegCost   uint64
			transfers   = newAccountState && !stk.Back(2).IsZero()
		)
		if !ibs.AddressInAccessList(addr) {
			ibs.AddAddressToAccessList(addr)
			accessCost = coldAccountAccess8038 - warmAccess8038
			if !contract.UseGas(accessCost) {
				return 0, ErrOutOfGas
			}
		}
		execCost, err := execFn(evm, contract, stk, mem, memorySize)
		if err != nil {
			return 0, err
		}
		if !contract.UseGas(execCost) {
			return 0, ErrOutOfGas
		}
		if target, ok := ParseDelegation(ibs.GetCode(addr)); ok {
			if ibs.AddressInAccessList(target) {
				delegCost = warmAccess8038
			} else {
				ibs.AddAddressToAccessList(target)
				delegCost = coldAccountAccess8038
			}
			if !contract.UseGas(delegCost) {
				return 0, ErrOutOfGas
			}
			// The target has passed its gas check: it is accessed (EIP-7928)
			// even if the call then fails on the sender balance or depth.
			ibs.GetCode(target)
		}
		if transfers && ibs.Empty(addr) {
			if !contract.chargeState(newAccountState8037()) {
				return 0, ErrOutOfGas
			}
		}
		callGasTemp, err := callGas(true, contract.Gas, 0, stk.Back(0))
		if err != nil {
			return 0, err
		}
		evm.SetCallGasTemp(callGasTemp)
		// Hand the direct charges back; the interpreter re-charges them
		// together with the forwarded gas.
		contract.Gas += accessCost + execCost + delegCost
		total, overflow := math.SafeAdd(accessCost+delegCost, execCost)
		if overflow {
			return 0, ErrGasUintOverflow
		}
		if total, overflow = math.SafeAdd(total, callGasTemp); overflow {
			return 0, ErrGasUintOverflow
		}
		return total, nil
	}
}

// newAccountState8037 is kept as a function so makeCallVariantGas8037's
// boolean parameter of the same name does not shadow the constant.
func newAccountState8037() uint64 { return newAccountState }

var (
	innerGasCall8037    = makeCallVariantGas8037(callValueExecutionGas, true)
	gasCallCode8038     = makeCallVariantGas8037(callValueExecutionGas, false)
	gasDelegateCall8038 = makeCallVariantGas8037(callNoValueExecutionGas, false)
	gasStaticCall8038   = makeCallVariantGas8037(callNoValueExecutionGas, false)
)

// gasCall8037 prices CALL. A value transfer in a static context is
// rejected before any account is touched, as go-ethereum does: the frame
// halts either way, but the EIP-7702 delegation lookup reads the callee's
// code, which would put it in the EIP-7928 block access list.
func gasCall8037(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	if !stk.Back(2).IsZero() && interpreterReadOnly(evm) {
		return 0, ErrWriteProtection
	}
	return innerGasCall8037(evm, contract, stk, mem, memorySize)
}

// interpreterReadOnly reports whether the running frame is static. An EVM
// that does not expose its interpreter reads as not static, leaving the
// rejection to the opcode.
func interpreterReadOnly(evm VMInterpreter) bool {
	if e, ok := evm.(interface{ Interpreter() Interpreter }); ok {
		if in, ok := e.Interpreter().(interface{ getReadonly() bool }); ok {
			return in.getReadonly()
		}
	}
	return false
}

// gasCreate8037 and gasCreate2_8037 are the CREATE/CREATE2 dynamic gas:
// memory and init-code words (the EIP-7954 cap applies). The new account's
// state gas is charged by create8037 once the destination is known to be
// empty.
var (
	gasCreate8037   = gasCreateEip3860
	gasCreate2_8037 = gasCreate2Eip3860
)

// gasSelfdestruct8037 prices SELFDESTRUCT under EIP-8037/8038: a cold
// beneficiary access, and for a balance swept into an empty account
// ACCOUNT_WRITE plus the new account's state gas. No refund (EIP-3529).
func gasSelfdestruct8037(evm VMInterpreter, contract *Contract, stk *stack.Stack, mem *Memory, memorySize uint64) (uint64, error) {
	var (
		gas     uint64
		ibs     = evm.IntraBlockState()
		address = types.Address(stk.Peek().Bytes20())
	)
	// Rejected in a static frame before the beneficiary is touched, so it
	// stays out of the EIP-7928 block access list.
	if interpreterReadOnly(evm) {
		return 0, ErrWriteProtection
	}
	if !ibs.AddressInAccessList(address) {
		ibs.AddAddressToAccessList(address)
		gas = coldAccountAccess8038
	}
	if contract.Gas < gas {
		return 0, ErrOutOfGas
	}
	if ibs.Empty(address) && !ibs.GetBalance(contract.Address()).IsZero() {
		gas += accountWrite8038
		if !contract.CanAffordState(gas, newAccountState) || !contract.chargeState(newAccountState) {
			return 0, ErrOutOfGas
		}
	}
	return gas, nil
}

// enterCall wraps a CALL-family opcode: the frame's reservoir goes to the
// child, and what the child leaves comes back after the call.
func enterCall(op executionFunc, pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	evm := interpreter.evm
	evm.ForwardStateGas(scope.Contract)
	ret, err := op(pc, interpreter, scope)
	evm.AbsorbStateGas(scope.Contract)
	return ret, err
}

func opCall8037(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	toAddr := types.Address(scope.Stack.Back(1).Bytes20())
	transfers := !scope.Stack.Back(2).IsZero()
	ret, err := enterCall(opCall, pc, interpreter, scope)
	if err != nil {
		return ret, err
	}
	// A failed call into an empty account refills the new-account state gas
	// charged for its value transfer.
	if transfers && scope.Stack.Peek().IsZero() && interpreter.evm.IntraBlockState().Empty(toAddr) {
		scope.Contract.refundState(newAccountState)
	}
	return ret, nil
}

func opCallCode8037(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	return enterCall(opCallCode, pc, interpreter, scope)
}

func opDelegateCall8037(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	return enterCall(opDelegateCall, pc, interpreter, scope)
}

func opStaticCall8037(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	return enterCall(opStaticCall, pc, interpreter, scope)
}

// createFramePreCheck reports whether a CREATE from caller can enter its
// frame at all (depth, balance, nonce). Under Amsterdam it runs in the
// creating frame, before the account-creation charge.
func createFramePreCheck(interpreter *EVMInterpreter, caller types.Address, value *uint256.Int) bool {
	evm := interpreter.evm
	if interpreter.Depth() > int(params.CallCreateDepth) {
		return false
	}
	if !evm.Context().CanTransfer(evm.IntraBlockState(), caller, value) {
		return false
	}
	return evm.IntraBlockState().GetNonce(caller) != ^uint64(0)
}

// create8037 is the shared body of CREATE and CREATE2 under Amsterdam. The
// stack has been popped down to the size slot (stackvalue), which receives
// the result.
func create8037(interpreter *EVMInterpreter, scope *ScopeContext, input []byte, value *uint256.Int, salt *uint256.Int, stackvalue *uint256.Int) ([]byte, error) {
	evm := interpreter.evm
	ibs := evm.IntraBlockState()
	c := scope.Contract
	caller := c.Address()

	var contractAddr types.Address
	if salt == nil {
		contractAddr = crypto.CreateAddress(caller, ibs.GetNonce(caller))
	} else {
		contractAddr = crypto.CreateAddress2(caller, salt.Bytes32(), crypto.Keccak256(input))
	}
	// A failed precheck ends the create frame only: the creating frame
	// keeps all its gas and sees a zero result.
	if !createFramePreCheck(interpreter, caller, value) {
		stackvalue.Clear()
		interpreter.returnData = nil
		return nil, nil
	}
	// The new account's state gas, charged in the creating frame before the
	// 63/64 split, and only for an empty destination.
	charged := false
	if ibs.Empty(contractAddr) {
		if !c.chargeState(newAccountState) {
			return nil, ErrOutOfGas
		}
		charged = true
	}
	gas := c.Gas
	gas -= gas / 64
	c.UseGas(gas)

	evm.ForwardStateGas(c)
	var (
		res       []byte
		addr      types.Address
		returnGas uint64
		suberr    error
	)
	if salt == nil {
		res, addr, returnGas, suberr = evm.Create(c, input, gas, value)
	} else {
		res, addr, returnGas, suberr = evm.Create2(c, input, gas, value, salt)
	}
	if suberr != nil {
		stackvalue.Clear()
	} else {
		stackvalue.SetBytes(addr.Bytes())
	}
	c.Gas += returnGas
	evm.AbsorbStateGas(c)
	// A failed creation (revert, halt or collision) refills the account
	// creation charge.
	if charged && suberr != nil {
		c.refundState(newAccountState)
	}
	if suberr == ErrExecutionReverted {
		interpreter.returnData = res
		return res, nil
	}
	interpreter.returnData = nil
	return nil, nil
}

func opCreate8037(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	if interpreter.readOnly {
		return nil, ErrWriteProtection
	}
	var (
		value  = scope.Stack.Pop()
		offset = scope.Stack.Pop()
		size   = scope.Stack.Peek()
		input  = scope.Memory.GetCopy(MustSafeUint64ToInt64(offset.Uint64()), MustSafeUint64ToInt64(size.Uint64()))
	)
	return create8037(interpreter, scope, input, &value, nil, size)
}

func opCreate2_8037(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	if interpreter.readOnly {
		return nil, ErrWriteProtection
	}
	var (
		endowment = scope.Stack.Pop()
		offset    = scope.Stack.Pop()
		size      = scope.Stack.Pop()
		salt      = scope.Stack.Pop()
		input     = scope.Memory.GetCopy(MustSafeUint64ToInt64(offset.Uint64()), MustSafeUint64ToInt64(size.Uint64()))
	)
	var result uint256.Int
	ret, err := create8037(interpreter, scope, input, &endowment, &salt, &result)
	if err != nil {
		return ret, err
	}
	scope.Stack.Push(&result)
	return ret, nil
}

// opSelfdestruct8246 is SELFDESTRUCT under Amsterdam. EIP-8246 removes the
// burn of a self-beneficiary: the balance stays, and an account created in
// this transaction is reduced to a balance-only account when the
// transaction is finalized (IntraBlockState.FinalizeTx). EIP-7708 logs a
// nonzero sweep to another account.
func opSelfdestruct8246(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	if interpreter.readOnly {
		return nil, ErrWriteProtection
	}
	var (
		ibs         = interpreter.evm.IntraBlockState()
		beneficiary = scope.Stack.Pop()
		this        = scope.Contract.Address()
		target      = types.Address(beneficiary.Bytes20())
		balance     = *ibs.GetBalance(this)
	)
	if interpreter.cfg.Debug {
		interpreter.cfg.Tracer.CaptureEnter(SELFDESTRUCT, this, target, []byte{}, 0, &balance)
		interpreter.cfg.Tracer.CaptureExit([]byte{}, 0, nil)
	}
	if this == target {
		ibs.Selfdestruct8246(this)
		return nil, errStopToken
	}
	ibs.AddBalance(target, &balance)
	if !balance.IsZero() {
		EmitTransferLog(ibs, this, target, &balance)
	}
	ibs.Selfdestruct6780(this, target)
	return nil, errStopToken
}
