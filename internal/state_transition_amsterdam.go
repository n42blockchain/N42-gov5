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
// Amsterdam (Glamsterdam) transaction execution: the EIP-8037 state-gas
// reservoir and two-dimensional block accounting, the EIP-2780 runtime
// charges for the top-level recipient and EIP-7702 authorizations, the
// EIP-7976/7981 calldata floor and the EIP-7778 refund rules. Ported from
// go-ethereum's core/state_transition.go; TransitionDb hands every
// Glamsterdam message here, so no other fork goes through this file.

package internal

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/params"
)

// errOutOfGasRuntime marks a runtime charge the transaction budget could
// not cover; the top frame then halts.
var errOutOfGasRuntime = errors.New("out of gas in runtime charge")

// intrinsicBaseGasEIP2780 is the EIP-2780 base of a transaction: the sender
// (12000), the recipient touch (3000, or CREATE_ACCESS for a creation, none
// for a self-transfer) and a value transfer's recipient write and log (6000).
func intrinsicBaseGasEIP2780(from types.Address, to *types.Address, value *uint256.Int) uint64 {
	isCreate := to == nil
	isSelf := to != nil && *to == from
	hasValue := value != nil && !value.IsZero()
	gas := params.TxBaseCostEIP2780
	switch {
	case isSelf:
	case isCreate:
		gas += params.CreateAccessEIP8038
	default:
		gas += params.TxColdAccountEIP2780
	}
	if hasValue && !isSelf && !isCreate {
		gas += params.TxValueCostEIP2780
	}
	return gas
}

// floorDataGasAmsterdam is the EIP-7623 calldata floor as changed by
// EIP-7976 (every calldata byte is four tokens at 16 gas) and EIP-7981
// (access-list entries count their bytes as tokens too), on the EIP-2780
// transaction base.
func floorDataGasAmsterdam(from types.Address, to *types.Address, value *uint256.Int, data []byte, accessList transaction.AccessList) (uint64, error) {
	const tokensPerByte = 4
	tokens := uint64(len(data)) * tokensPerByte
	tokens += uint64(len(accessList)) * params.AccessListAddressBytes * tokensPerByte
	tokens += uint64(accessList.StorageKeys()) * params.AccessListStorageKeyBytes * tokensPerByte
	base := intrinsicBaseGasEIP2780(from, to, value)
	if tokens > (^uint64(0)-base)/params.TotalCostFloorPerTokenEIP7976 {
		return 0, ErrGasUintOverflow
	}
	return base + tokens*params.TotalCostFloorPerTokenEIP7976, nil
}

// transitionAmsterdam executes a Glamsterdam message after preCheck has
// bought its gas.
func (st *StateTransition) transitionAmsterdam(refunds bool, gasBailout bool) (*ExecutionResult, error) {
	var (
		msg              = st.msg
		rules            = st.evm.ChainRules()
		sender           = vm2.AccountRef(msg.From())
		contractCreation = msg.To() == nil
		value            = st.value
	)
	if value == nil {
		value = new(uint256.Int)
	}
	hasValue := !value.IsZero()
	isSelfTransfer := !contractCreation && *msg.To() == msg.From()

	intrinsic, err := IntrinsicGas(st.data, msg.AccessList(), msg.AuthList(), contractCreation, rules.IsHomestead, rules.IsIstanbul, rules.IsShanghai, rules.IsPrague, true, hasValue, isSelfTransfer)
	if err != nil {
		return nil, err
	}
	if st.gas < intrinsic {
		return nil, fmt.Errorf("%w: have %d, want %d", ErrIntrinsicGas, st.gas, intrinsic)
	}
	floorDataGas, err := floorDataGasAmsterdam(msg.From(), msg.To(), value, st.data, msg.AccessList())
	if err != nil {
		return nil, err
	}
	if st.initialGas < floorDataGas {
		return nil, fmt.Errorf("%w: have %d, want %d", ErrFloorDataGas, st.initialGas, floorDataGas)
	}
	// The gas limit may exceed TX_MAX_GAS_LIMIT (the excess feeds the state
	// reservoir), but the intrinsic cost and the floor may not.
	if max(intrinsic, floorDataGas) > fixedgas.MaxTxnGasLimit {
		return nil, fmt.Errorf("%w: intrinsic cost %d, floor %d", ErrFloorDataGas, intrinsic, floorDataGas)
	}
	if contractCreation && uint64(len(st.data)) > params.MaxInitCodeSizeEIP7954 {
		return nil, fmt.Errorf("%w: code size %d limit %d", ErrMaxInitCodeSizeExceeded, len(st.data), params.MaxInitCodeSizeEIP7954)
	}
	// The sender must still afford the top frame's value after the gas
	// purchase.
	if hasValue && !gasBailout && !st.evm.Context().CanTransfer(st.state, msg.From(), value) {
		return nil, fmt.Errorf("%w: address %v", ErrInsufficientFundsForTransfer, msg.From().Hex())
	}

	st.state.PrepareAccessList(msg.From(), msg.To(), vm2.ActivePrecompiles(rules), msg.AccessList())
	st.state.AddAddressToAccessList(st.evm.Context().Coinbase)

	// EIP-8037: the gas left after the intrinsic cost is split into the
	// execution budget, capped at TX_MAX_GAS_LIMIT - intrinsic, and the
	// state-gas reservoir that takes the rest.
	evmGas := st.gas - intrinsic
	st.gas = minGas(fixedgas.MaxTxnGasLimit-intrinsic, evmGas)
	budget := vm2.StateGasBudget{Reservoir: evmGas - st.gas}

	var (
		ret   []byte
		vmerr error
	)
	if contractCreation {
		ret, vmerr = st.executeCreateAmsterdam(sender, value, &budget)
	} else {
		ret, vmerr = st.executeCallAmsterdam(sender, value, hasValue, gasBailout, &budget)
	}

	// Settle: tx_gas_used_before_refund = tx.gas - gas_left - reservoir.
	if budget.Used < 0 {
		return nil, fmt.Errorf("negative top frame state gas usage %d", budget.Used)
	}
	txState := uint64(budget.Used)
	gasLeft := st.gas + budget.Reservoir
	usedBeforeRefund := st.initialGas - gasLeft
	if usedBeforeRefund < txState {
		return nil, fmt.Errorf("negative top frame execution gas usage: total %d, state %d", usedBeforeRefund, txState)
	}
	txExec := max(usedBeforeRefund-txState, floorDataGas)

	gasUsed := usedBeforeRefund
	if refunds {
		refund := minGas(usedBeforeRefund/params.RefundQuotientEIP3529, st.state.GetRefund())
		gasLeft += refund
		gasUsed -= refund
	}
	if gasUsed < floorDataGas {
		gasLeft -= floorDataGas - gasUsed
		gasUsed = floorDataGas
	}
	if err := st.gp.ChargeGasAmsterdam(txExec, txState); err != nil && !gasBailout {
		return nil, err
	}
	st.gas = gasLeft
	if gasLeft > 0 {
		st.state.AddBalance(msg.From(), new(uint256.Int).Mul(uint256.NewInt(gasLeft), st.gasPrice))
	}

	effectiveTip := st.gasPrice
	if rules.IsLondon {
		baseFee := st.evm.Context().BaseFee
		if st.gasFeeCap.Gt(baseFee) {
			effectiveTip = new(uint256.Int).Sub(st.gasFeeCap, baseFee)
			if st.tip.Lt(effectiveTip) {
				effectiveTip = st.tip
			}
		} else {
			effectiveTip = new(uint256.Int)
		}
	}
	amount := new(uint256.Int).SetUint64(gasUsed)
	amount.Mul(amount, effectiveTip)
	st.creditFee(st.policy.priorityFeeRecipient(st.evm.Context().Coinbase), amount)
	if st.policy.shouldCollectEIP1559Fee(msg, rules) {
		burnt := new(uint256.Int).Mul(new(uint256.Int).SetUint64(gasUsed), st.evm.Context().BaseFee)
		st.creditFee(*st.evm.ChainConfig().Eip1559FeeCollector, burnt)
	}
	return &ExecutionResult{
		UsedGas:      gasUsed,
		BlockGasUsed: gasUsed,
		TxExecGas:    txExec,
		TxStateGas:   txState,
		Err:          vmerr,
		ReturnData:   ret,
	}, nil
}

// haltTopFrame ends the transaction before its first frame runs: execution
// gas is burned, the reservoir is kept.
func haltTopFrame(gas *uint64, budget *vm2.StateGasBudget) error {
	*gas, *budget = budget.Exit(*gas, vm2.ErrOutOfGas)
	return vm2.ErrOutOfGas
}

func (st *StateTransition) executeCreateAmsterdam(sender vm2.AccountRef, value *uint256.Int, budget *vm2.StateGasBudget) ([]byte, error) {
	from := st.msg.From()
	addr := crypto.CreateAddress(from, st.state.GetNonce(from))
	charged := false
	if st.state.Empty(addr) {
		if !budget.Charge(&st.gas, vm2.NewAccountStateGas) {
			// The nonce bump evm.Create would do still happens.
			st.state.SetNonce(from, st.state.GetNonce(from)+1)
			return nil, haltTopFrame(&st.gas, budget)
		}
		charged = true
	}
	st.evm.SetStateGasReservoir(budget.Reservoir)
	budget.Reservoir = 0
	ret, _, left, vmerr := st.evm.Create(sender, st.data, st.gas, value)
	st.gas = left
	budget.Absorb(&st.gas, st.evm.StateGasLeftover())
	if charged && vmerr != nil {
		budget.Refund(&st.gas, vm2.NewAccountStateGas)
	}
	if vmerr != nil && vmerr != vm2.ErrExecutionReverted {
		st.gas = 0
	}
	return ret, vmerr
}

func (st *StateTransition) executeCallAmsterdam(sender vm2.AccountRef, value *uint256.Int, hasValue, bailout bool, budget *vm2.StateGasBudget) ([]byte, error) {
	from, to := st.msg.From(), st.to()
	st.state.SetNonce(from, st.state.GetNonce(from)+1)

	snapshot := st.state.Snapshot()
	if !st.applyAuthorizationsAmsterdam(hasValue, budget) || !st.chargeCallRecipient(value, budget) {
		st.state.RevertToSnapshot(snapshot)
		return nil, haltTopFrame(&st.gas, budget)
	}
	st.evm.SetStateGasReservoir(budget.Reservoir)
	budget.Reservoir = 0
	ret, left, vmerr := st.evm.Call(sender, to, st.data, st.gas, value, bailout)
	st.gas = left
	budget.Absorb(&st.gas, st.evm.StateGasLeftover())
	if vmerr != nil && hasValue && st.state.Empty(to) {
		budget.Refund(&st.gas, vm2.NewAccountStateGas)
	}
	if vmerr != nil && vmerr != vm2.ErrExecutionReverted {
		st.gas = 0
	}
	return ret, vmerr
}

// chargeRuntime charges an execution cost followed by a state cost.
func (st *StateTransition) chargeRuntime(budget *vm2.StateGasBudget, exec, state uint64) bool {
	if !budget.CanAfford(st.gas, exec, state) {
		return false
	}
	st.gas -= exec
	return budget.Charge(&st.gas, state)
}

// chargeCallRecipient applies the EIP-2780 runtime charges for the
// top-level recipient before the first frame: the new account's state gas
// for a value transfer into an empty account, and the access to an
// EIP-7702 delegation target.
func (st *StateTransition) chargeCallRecipient(value *uint256.Int, budget *vm2.StateGasBudget) bool {
	to := st.to()
	if !value.IsZero() && st.state.Empty(to) {
		if !st.chargeRuntime(budget, 0, vm2.NewAccountStateGas) {
			return false
		}
	}
	if target, ok := vm2.ParseDelegation(st.state.GetCode(to)); ok {
		cost := params.ColdAccountAccessEIP8038
		if st.state.AddressInAccessList(target) {
			cost = params.WarmStorageReadCostEIP2929
		}
		if !st.chargeRuntime(budget, cost, 0) {
			return false
		}
		st.state.AddAddressToAccessList(target)
		st.state.GetCode(target)
	}
	return true
}

// authTracking records the charges already paid for an authority by
// earlier authorizations of the same transaction.
type authTracking struct {
	written         bool // ACCOUNT_WRITE paid
	authBaseCovered bool // delegation designator present at tx start, or paid
}

// applyAuthorizationsAmsterdam applies the EIP-7702 authorizations with the
// EIP-2780/8037/8038 runtime charges. It reports false when the budget
// cannot cover a charge; the caller then halts the top frame.
func (st *StateTransition) applyAuthorizationsAmsterdam(hasValue bool, budget *vm2.StateGasBudget) bool {
	authList := st.msg.AuthList()
	if len(authList) == 0 {
		return true
	}
	from, to := st.msg.From(), st.to()
	authorities := make(map[types.Address]*authTracking)
	for _, auth := range authList {
		if auth == nil {
			continue
		}
		if !auth.ChainID.IsZero() && auth.ChainID.CmpBig(st.evm.ChainConfig().ChainID) != 0 {
			continue
		}
		if auth.Nonce+1 < auth.Nonce {
			continue
		}
		authority, err := auth.RecoverSigner()
		if err != nil {
			continue
		}
		st.state.AddAddressToAccessList(authority)
		code := st.state.GetCode(authority)
		oldDelegation, curDelegated := vm2.ParseDelegation(code)
		if len(code) != 0 && !curDelegated {
			continue
		}
		if st.state.GetNonce(authority) != auth.Nonce {
			continue
		}

		track := authorities[authority]
		if track == nil {
			track = &authTracking{authBaseCovered: curDelegated}
			authorities[authority] = track
		}
		var execCost, stateCost uint64
		// The first paid write to the authority account. The sender's and,
		// for a value transfer, the recipient's are prepaid by the
		// intrinsic cost.
		if !track.written && authority != from && (authority != to || !hasValue) {
			execCost += params.AccountWriteEIP8038
			track.written = true
		}
		if st.state.Empty(authority) {
			stateCost += vm2.NewAccountStateGas
		}
		if auth.Address != (types.Address{}) && !track.authBaseCovered {
			stateCost += vm2.DelegationStateGas
			track.authBaseCovered = true
		}
		if !st.chargeRuntime(budget, execCost, stateCost) {
			return false
		}
		st.state.SetNonce(authority, auth.Nonce+1)
		if auth.Address == (types.Address{}) {
			if curDelegated {
				st.state.SetCode(authority, nil)
			}
			continue
		}
		if !curDelegated || auth.Address != oldDelegation {
			st.state.SetCode(authority, vm2.AddressToDelegation(auth.Address))
		}
	}
	return true
}

// minGas is a uint64 min. The builtin is shadowed inside this package's test
// build by a helper of the same name, so it cannot be used here.
func minGas(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}
