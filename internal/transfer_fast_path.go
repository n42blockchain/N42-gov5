// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Transfer fast path (P3, docs/QS_MULTI_MILLION_DESIGN.md 4.3): a plain
// value transfer between EOAs applied as nonce and balance arithmetic on the
// transaction's IntraBlockState, without the StateTransition, the intrinsic
// gas computation, the access-list setup or the EVM call frame.
//
// The fast path issues the same IntraBlockState operations, in the same
// order, as the EVM path does for such a transaction (preCheck, buyGas, the
// nonce bump, evm.Call's Exist + Transfer, refundGas, the fee credits), so
// the state diff, the reads recorded by the Block-STM view and the receipt
// are the EVM path's. Every check runs before the first write; any condition
// not met returns false with nothing written and the caller runs the EVM
// path in the same incarnation, which also produces the exact error for an
// invalid transaction.
//
// N42_TRANSFER_FAST_PATH=0 disables it (A/B); default on.

package internal

import (
	"os"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	cmath "github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// transferFastPath is read once: flipping it mid-process would split a
// node's blocks across two code paths for no benefit.
var transferFastPath = os.Getenv("N42_TRANSFER_FAST_PATH") != "0"

// fastTransfers counts the transactions the fast path applied (process
// lifetime; tests and diagnostics).
var fastTransfers atomic.Uint64

// fastTransferScratch holds a worker's per-transaction values for the fast
// path, reused across its transactions (the IntraBlockState copies every
// amount it is handed, and the result is read before the next transaction).
type fastTransferScratch struct {
	mgval, balanceCheck, zero, tip, amount, burn uint256.Int
	result                                       ExecutionResult
}

// tryFastTransfer applies msg as a plain transfer when it is one. It returns
// (result, true) when the transaction was applied, and (nil, false) with no
// state written and gp untouched otherwise. sc may be nil; the result points
// into it and is valid until its next use.
func tryFastTransfer(evm *vm2.EVM, ibs *state.IntraBlockState, msg *transaction.Message, gp *common.GasPool, sink FeeSink, sc *fastTransferScratch) (*ExecutionResult, bool) {
	// Shape checks: nothing here reads state.
	to := msg.To()
	if to == nil || len(msg.Data()) != 0 || len(msg.AccessList()) != 0 || len(msg.AuthList()) != 0 ||
		len(msg.BlobHashes()) != 0 || msg.IsFree() || !msg.CheckNonce() || msg.Gas() != params.TxGas {
		return nil, false
	}
	cfg := evm.Config()
	if cfg.Debug || cfg.RestoreState || dbgNonceBlock > 0 || dbgRefundBlock > 0 {
		return nil, false
	}
	rules := evm.ChainRules()
	// Amsterdam changes the intrinsic gas of a transfer (EIP-2780) and adds
	// a transfer log (EIP-7708): left to the EVM path.
	if rules.IsGlamsterdam {
		return nil, false
	}
	chainCfg := evm.ChainConfig()
	policy := newTransitionChainPolicy(chainCfg)
	if policy.shouldForceGasBailout() {
		return nil, false
	}
	from := msg.From()
	gasPrice, feeCap, tip, value := msg.ExecutionValues()
	gas := msg.Gas()
	intrinsic, err := IntrinsicGas(nil, nil, nil, false, rules.IsHomestead, rules.IsIstanbul, rules.IsShanghai, rules.IsPrague, false, !value.IsZero(), *to == from)
	if err != nil || intrinsic != gas {
		return nil, false
	}
	if rules.IsPrague && vm2.FloorDataGas(nil) > gas {
		return nil, false
	}
	if evm.IsPrecompile(*to) {
		return nil, false
	}
	blockCtx := evm.Context()
	if rules.IsLondon && (!cfg.NoBaseFee || !feeCap.IsZero() || !tip.IsZero()) {
		if CheckEip1559TxGasFeeCap(from, feeCap, tip, blockCtx.BaseFee, false) != nil {
			return nil, false
		}
	}
	if sc == nil {
		sc = new(fastTransferScratch)
	}
	mgval, balanceCheck := &sc.mgval, &sc.balanceCheck
	mgval.SetUint64(gas)
	if _, overflow := mgval.MulOverflow(mgval, gasPrice); overflow {
		return nil, false
	}
	balanceCheck.SetUint64(gas)
	if _, overflow := balanceCheck.MulOverflow(balanceCheck, feeCap); overflow {
		return nil, false
	}
	if _, overflow := balanceCheck.AddOverflow(balanceCheck, value); overflow {
		return nil, false
	}

	// State checks, in the EVM path's read order (preCheck: nonce, code
	// hash, balance; then the recipient).
	nonce := ibs.GetNonce(from)
	if nonce != msg.Nonce() || nonce+1 < nonce {
		return nil, false
	}
	if ch := ibs.GetCodeHash(from); ch != emptyCodeHash && ch != (types.Hash{}) {
		return nil, false // delegated (EIP-7702) or contract sender
	}
	if ibs.GetBalance(from).Lt(balanceCheck) {
		return nil, false
	}
	// A recipient that does not exist yet is created (or, for a zero value,
	// left untouched) by evm.Call: left to the EVM path.
	if !ibs.Exist(*to) {
		return nil, false
	}
	if ch := ibs.GetCodeHash(*to); ch != emptyCodeHash && ch != (types.Hash{}) {
		return nil, false
	}
	if gp.SubGas(gas) != nil {
		return nil, false
	}

	// Writes, in the EVM path's order: buyGas, the nonce bump, the value
	// transfer (CanTransfer holds: the balance covers gas*feeCap + value and
	// gas*gasPrice <= gas*feeCap was debited), the zero refund of refundGas.
	ibs.SubBalance(from, mgval)
	ibs.SetNonce(from, nonce+1)
	blockCtx.Transfer(ibs, from, *to, value, false)
	sc.zero.Clear()
	ibs.AddBalance(from, &sc.zero) // refundGas: no gas left, no refund counter
	gp.AddGas(0)

	// Fee credits, exactly as TransitionDb computes them.
	effectiveTip := gasPrice
	if rules.IsLondon {
		if feeCap.Gt(blockCtx.BaseFee) {
			effectiveTip = cmath.Min256(tip, sc.tip.Sub(feeCap, blockCtx.BaseFee))
		} else {
			effectiveTip = u256.Num0
		}
	}
	amount := sc.amount.SetUint64(gas)
	amount.Mul(amount, effectiveTip)
	creditFeeTo(ibs, sink, policy.priorityFeeRecipient(blockCtx.Coinbase), amount)
	if policy.shouldCollectEIP1559Fee(msg, rules) {
		burnAmount := sc.burn.SetUint64(gas)
		burnAmount.Mul(burnAmount, blockCtx.BaseFee)
		creditFeeTo(ibs, sink, *chainCfg.Eip1559FeeCollector, burnAmount)
	}
	fastTransfers.Add(1)
	sc.result = ExecutionResult{UsedGas: gas, BlockGasUsed: gas}
	return &sc.result, true
}

// creditFeeTo is StateTransition.creditFee without a StateTransition.
func creditFeeTo(ibs *state.IntraBlockState, sink FeeSink, recipient types.Address, amount *uint256.Int) {
	if sink != nil {
		sink(recipient, amount)
		return
	}
	ibs.AddBalance(recipient, amount)
}
