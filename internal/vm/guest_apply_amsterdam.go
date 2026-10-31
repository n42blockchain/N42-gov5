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
// Amsterdam (Glamsterdam) message application for the zkVM guest: a copy of
// the host's internal.StateTransition.transitionAmsterdam (the EIP-8037
// state-gas reservoir and two-dimensional block accounting, the EIP-2780
// runtime charges, EIP-7702 authorizations, the EIP-7976/7981 floor and the
// EIP-7778 refund rules) that does not import package internal. Any change
// to the host path must be made here too; the differential test in
// internal/zkguest_amsterdam_test.go runs both on the same transactions.

package vm

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/params"
)

var (
	errGuestFloorDataGas      = errors.New("vm: insufficient gas for the calldata floor in guest execution")
	errGuestGasLimitTooHigh   = errors.New("vm: transaction gas limit above the EIP-8037 cap in guest execution")
	errGuestInitCodeSize      = errors.New("vm: initcode size above the EIP-7954 limit in guest execution")
	errGuestTransferFunds     = errors.New("vm: insufficient funds for the transfer in guest execution")
	errGuestGasUintOverflow   = errors.New("vm: gas uint64 overflow in guest execution")
	errGuestNegativeStateGas  = errors.New("vm: negative top frame state gas in guest execution")
	errGuestNegativeExecution = errors.New("vm: negative top frame execution gas in guest execution")
)

// guestFloorDataGasAmsterdam is the EIP-7623 calldata floor as changed by
// EIP-7976 and EIP-7981, on the EIP-2780 transaction base.
func guestFloorDataGasAmsterdam(from types.Address, to *types.Address, value *uint256.Int, data []byte, accessList transaction.AccessList) (uint64, error) {
	const tokensPerByte = 4
	tokens := uint64(len(data)) * tokensPerByte
	tokens += uint64(len(accessList)) * params.AccessListAddressBytes * tokensPerByte
	tokens += uint64(accessList.StorageKeys()) * params.AccessListStorageKeyBytes * tokensPerByte

	isCreate := to == nil
	isSelf := to != nil && *to == from
	base := params.TxBaseCostEIP2780
	switch {
	case isSelf:
	case isCreate:
		base += params.CreateAccessEIP8038
	default:
		base += params.TxColdAccountEIP2780
	}
	if value != nil && !value.IsZero() && !isSelf && !isCreate {
		base += params.TxValueCostEIP2780
	}
	if tokens > (^uint64(0)-base)/params.TotalCostFloorPerTokenEIP7976 {
		return 0, errGuestGasUintOverflow
	}
	return base + tokens*params.TotalCostFloorPerTokenEIP7976, nil
}

// applyMessageForGuestAmsterdam applies a Glamsterdam message: it buys the
// gas, checks the block has room in both EIP-8037 dimensions, runs the top
// frame with the execution budget and state reservoir, and settles the
// transaction's execution and state gas into gp.
func applyMessageForGuestAmsterdam(evm *EVM, msg transaction.Message, gp *common.GasPool) (*GuestExecutionResult, error) {
	var (
		ibs      = evm.IntraBlockState()
		rules    = evm.ChainRules()
		from     = msg.From()
		gasLimit = msg.Gas()
		gasPrice = msg.GasPrice()
		value    = msg.Value()
	)
	if value == nil {
		value = new(uint256.Int)
	}
	if msg.CheckNonce() && msg.Nonce() != ibs.GetNonce(from) {
		return nil, errGuestNonceMismatch
	}
	if gasLimit > params.MaxTxTotalGasEIP8037 {
		return nil, fmt.Errorf("%w (cap: %d, tx: %d)", errGuestGasLimitTooHigh, params.MaxTxTotalGasEIP8037, gasLimit)
	}

	// Buy gas. The pool is only checked here and charged at settlement.
	mgval := new(uint256.Int).SetUint64(gasLimit)
	mgval.Mul(mgval, gasPrice)
	if have := ibs.GetBalance(from); have.Cmp(mgval) < 0 {
		return nil, fmt.Errorf("%w: have %v, want %v", errGuestBuyGas, have, mgval)
	}
	if err := gp.CheckGasAmsterdam(min(gasLimit, fixedgas.MaxTxnGasLimit), gasLimit); err != nil {
		return nil, err
	}
	ibs.SubBalance(from, mgval)

	contractCreation := msg.To() == nil
	hasValue := !value.IsZero()
	isSelfTransfer := !contractCreation && *msg.To() == from

	intrinsic := guestIntrinsicGas(msg.Data(), msg.AccessList(), contractCreation, rules, hasValue, isSelfTransfer)
	if n := uint64(len(msg.AuthList())); n > 0 {
		if (^uint64(0)-intrinsic)/params.TxAuthBaseCostEIP2780 < n {
			return nil, errGuestGasUintOverflow
		}
		intrinsic += n * params.TxAuthBaseCostEIP2780
	}
	if gasLimit < intrinsic {
		return nil, fmt.Errorf("%w: have %d, want %d", errGuestIntrinsicGas, gasLimit, intrinsic)
	}
	floorDataGas, err := guestFloorDataGasAmsterdam(from, msg.To(), value, msg.Data(), msg.AccessList())
	if err != nil {
		return nil, err
	}
	if gasLimit < floorDataGas {
		return nil, fmt.Errorf("%w: have %d, want %d", errGuestFloorDataGas, gasLimit, floorDataGas)
	}
	// The gas limit may exceed TX_MAX_GAS_LIMIT (the excess feeds the state
	// reservoir), but the intrinsic cost and the floor may not.
	if max(intrinsic, floorDataGas) > fixedgas.MaxTxnGasLimit {
		return nil, fmt.Errorf("%w: intrinsic cost %d, floor %d", errGuestFloorDataGas, intrinsic, floorDataGas)
	}
	if contractCreation && uint64(len(msg.Data())) > params.MaxInitCodeSizeEIP7954 {
		return nil, fmt.Errorf("%w: code size %d limit %d", errGuestInitCodeSize, len(msg.Data()), params.MaxInitCodeSizeEIP7954)
	}
	if hasValue && !evm.Context().CanTransfer(ibs, from, value) {
		return nil, fmt.Errorf("%w: address %v", errGuestTransferFunds, from.Hex())
	}

	ibs.PrepareAccessList(from, msg.To(), ActivePrecompiles(rules), msg.AccessList())
	ibs.AddAddressToAccessList(evm.Context().Coinbase)

	// EIP-8037: the gas left after the intrinsic cost is split into the
	// execution budget, capped at TX_MAX_GAS_LIMIT - intrinsic, and the
	// state-gas reservoir that takes the rest.
	evmGas := gasLimit - intrinsic
	g := &guestAmsterdamFrame{evm: evm, msg: msg, gas: min(fixedgas.MaxTxnGasLimit-intrinsic, evmGas)}
	budget := StateGasBudget{Reservoir: evmGas - g.gas}

	var (
		ret   []byte
		vmerr error
	)
	if contractCreation {
		ret, vmerr = g.executeCreate(value, &budget)
	} else {
		ret, vmerr = g.executeCall(value, hasValue, &budget)
	}

	// Settle: tx_gas_used_before_refund = tx.gas - gas_left - reservoir.
	if budget.Used < 0 {
		return nil, fmt.Errorf("%w: %d", errGuestNegativeStateGas, budget.Used)
	}
	txState := uint64(budget.Used)
	gasLeft := g.gas + budget.Reservoir
	usedBeforeRefund := gasLimit - gasLeft
	if usedBeforeRefund < txState {
		return nil, fmt.Errorf("%w: total %d, state %d", errGuestNegativeExecution, usedBeforeRefund, txState)
	}
	txExec := max(usedBeforeRefund-txState, floorDataGas)

	gasUsed := usedBeforeRefund
	refund := min(usedBeforeRefund/params.RefundQuotientEIP3529, ibs.GetRefund())
	gasLeft += refund
	gasUsed -= refund
	if gasUsed < floorDataGas {
		gasLeft -= floorDataGas - gasUsed
		gasUsed = floorDataGas
	}
	if err := gp.ChargeGasAmsterdam(txExec, txState); err != nil {
		return nil, err
	}
	if gasLeft > 0 {
		ibs.AddBalance(from, new(uint256.Int).Mul(uint256.NewInt(gasLeft), gasPrice))
	}

	// The tip goes to the coinbase, as in ApplyMessageForGuest: gasPrice is
	// already the effective gas price from AsMessage.
	effectiveTip := gasPrice
	if rules.IsLondon && evm.Context().BaseFee != nil {
		baseFee := evm.Context().BaseFee
		if gasPrice.Gt(baseFee) {
			effectiveTip = new(uint256.Int).Sub(gasPrice, baseFee)
		} else {
			effectiveTip = new(uint256.Int)
		}
	}
	ibs.AddBalance(evm.Context().Coinbase, new(uint256.Int).Mul(new(uint256.Int).SetUint64(gasUsed), effectiveTip))

	return &GuestExecutionResult{
		UsedGas:    gasUsed,
		Err:        vmerr,
		ReturnData: ret,
	}, nil
}

// guestAmsterdamFrame holds the top frame's execution gas while the
// runtime charges and the first frame draw on it.
type guestAmsterdamFrame struct {
	evm *EVM
	msg transaction.Message
	gas uint64
}

func (g *guestAmsterdamFrame) to() types.Address {
	if to := g.msg.To(); to != nil {
		return *to
	}
	return types.Address{}
}

// halt ends the transaction before its first frame runs: execution gas is
// burned, the reservoir is kept.
func (g *guestAmsterdamFrame) halt(budget *StateGasBudget) error {
	g.gas, *budget = budget.Exit(g.gas, ErrOutOfGas)
	return ErrOutOfGas
}

func (g *guestAmsterdamFrame) executeCreate(value *uint256.Int, budget *StateGasBudget) ([]byte, error) {
	ibs := g.evm.IntraBlockState()
	from := g.msg.From()
	addr := crypto.CreateAddress(from, ibs.GetNonce(from))
	charged := false
	if ibs.Empty(addr) {
		if !budget.Charge(&g.gas, NewAccountStateGas) {
			// The nonce bump Create would do still happens.
			ibs.SetNonce(from, ibs.GetNonce(from)+1)
			return nil, g.halt(budget)
		}
		charged = true
	}
	g.evm.SetStateGasReservoir(budget.Reservoir)
	budget.Reservoir = 0
	ret, _, left, vmerr := g.evm.Create(AccountRef(from), g.msg.Data(), g.gas, value)
	g.gas = left
	budget.Absorb(&g.gas, g.evm.StateGasLeftover())
	if charged && vmerr != nil {
		budget.Refund(&g.gas, NewAccountStateGas)
	}
	if vmerr != nil && vmerr != ErrExecutionReverted {
		g.gas = 0
	}
	return ret, vmerr
}

func (g *guestAmsterdamFrame) executeCall(value *uint256.Int, hasValue bool, budget *StateGasBudget) ([]byte, error) {
	ibs := g.evm.IntraBlockState()
	from, to := g.msg.From(), g.to()
	ibs.SetNonce(from, ibs.GetNonce(from)+1)

	snapshot := ibs.Snapshot()
	if !g.applyAuthorizations(hasValue, budget) || !g.chargeCallRecipient(value, budget) {
		ibs.RevertToSnapshot(snapshot)
		return nil, g.halt(budget)
	}
	g.evm.SetStateGasReservoir(budget.Reservoir)
	budget.Reservoir = 0
	ret, left, vmerr := g.evm.Call(AccountRef(from), to, g.msg.Data(), g.gas, value, false)
	g.gas = left
	budget.Absorb(&g.gas, g.evm.StateGasLeftover())
	if vmerr != nil && hasValue && ibs.Empty(to) {
		budget.Refund(&g.gas, NewAccountStateGas)
	}
	if vmerr != nil && vmerr != ErrExecutionReverted {
		g.gas = 0
	}
	return ret, vmerr
}

// chargeRuntime charges an execution cost followed by a state cost.
func (g *guestAmsterdamFrame) chargeRuntime(budget *StateGasBudget, exec, state uint64) bool {
	if !budget.CanAfford(g.gas, exec, state) {
		return false
	}
	g.gas -= exec
	return budget.Charge(&g.gas, state)
}

// chargeCallRecipient applies the EIP-2780 runtime charges for the
// top-level recipient: the new account's state gas for a value transfer
// into an empty account, and the access to an EIP-7702 delegation target.
func (g *guestAmsterdamFrame) chargeCallRecipient(value *uint256.Int, budget *StateGasBudget) bool {
	ibs := g.evm.IntraBlockState()
	to := g.to()
	if !value.IsZero() && ibs.Empty(to) {
		if !g.chargeRuntime(budget, 0, NewAccountStateGas) {
			return false
		}
	}
	if target, ok := ParseDelegation(ibs.GetCode(to)); ok {
		cost := params.ColdAccountAccessEIP8038
		if ibs.AddressInAccessList(target) {
			cost = params.WarmStorageReadCostEIP2929
		}
		if !g.chargeRuntime(budget, cost, 0) {
			return false
		}
		ibs.AddAddressToAccessList(target)
		ibs.GetCode(target)
	}
	return true
}

// guestAuthTracking records the charges already paid for an authority by
// earlier authorizations of the same transaction.
type guestAuthTracking struct {
	written         bool // ACCOUNT_WRITE paid
	authBaseCovered bool // delegation designator present at tx start, or paid
}

// applyAuthorizations applies the EIP-7702 authorizations with the
// EIP-2780/8037/8038 runtime charges. It reports false when the budget
// cannot cover a charge; the caller then halts the top frame.
func (g *guestAmsterdamFrame) applyAuthorizations(hasValue bool, budget *StateGasBudget) bool {
	authList := g.msg.AuthList()
	if len(authList) == 0 {
		return true
	}
	ibs := g.evm.IntraBlockState()
	from, to := g.msg.From(), g.to()
	authorities := make(map[types.Address]*guestAuthTracking)
	for _, auth := range authList {
		if auth == nil {
			continue
		}
		if !auth.ChainID.IsZero() && auth.ChainID.CmpBig(g.evm.ChainConfig().ChainID) != 0 {
			continue
		}
		if auth.Nonce+1 < auth.Nonce {
			continue
		}
		authority, err := auth.RecoverSigner()
		if err != nil {
			continue
		}
		ibs.AddAddressToAccessList(authority)
		code := ibs.GetCode(authority)
		oldDelegation, curDelegated := ParseDelegation(code)
		if len(code) != 0 && !curDelegated {
			continue
		}
		if ibs.GetNonce(authority) != auth.Nonce {
			continue
		}

		track := authorities[authority]
		if track == nil {
			track = &guestAuthTracking{authBaseCovered: curDelegated}
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
		if ibs.Empty(authority) {
			stateCost += NewAccountStateGas
		}
		if auth.Address != (types.Address{}) && !track.authBaseCovered {
			stateCost += DelegationStateGas
			track.authBaseCovered = true
		}
		if !g.chargeRuntime(budget, execCost, stateCost) {
			return false
		}
		ibs.SetNonce(authority, auth.Nonce+1)
		if auth.Address == (types.Address{}) {
			if curDelegated {
				ibs.SetCode(authority, nil)
			}
			continue
		}
		if !curDelegated || auth.Address != oldDelegation {
			ibs.SetCode(authority, AddressToDelegation(auth.Address))
		}
	}
	return true
}
