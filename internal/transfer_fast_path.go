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
	"bytes"
	"os"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	cmath "github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/internal/parallel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// transferFastPath is read once: flipping it mid-process would split a
// node's blocks across two code paths for no benefit.
var transferFastPath = os.Getenv("N42_TRANSFER_FAST_PATH") != "0"

// transferDirectPath lets a Block-STM worker apply a fast-path transfer on
// its reader and writer without the IntraBlockState (tryDirectTransfer).
// N42_TRANSFER_DIRECT=0 keeps the fast path on the IntraBlockState (A/B).
var transferDirectPath = os.Getenv("N42_TRANSFER_DIRECT") != "0"

// fastTransfers counts the transactions the fast path applied (process
// lifetime; tests and diagnostics). A worker counts into its scratch and
// adds the total here when it is done (flush): one shared atomic add per
// transaction was a cache line every worker bounced.
var fastTransfers atomic.Uint64

// directTransfers counts the fast-path transfers applied by
// tryDirectTransfer (a subset of fastTransfers; tests and diagnostics).
var directTransfers atomic.Uint64

// fastTransferScratch holds a worker's per-transaction values for the fast
// path, reused across its transactions (the IntraBlockState copies every
// amount it is handed, and the result is read before the next transaction).
type fastTransferScratch struct {
	mgval, balanceCheck, zero, tip, amount, burn uint256.Int
	result                                       ExecutionResult
	applied                                      uint64 // not yet added to fastTransfers
	appliedDirect                                uint64 // not yet added to directTransfers
	direct                                       directTransferAccounts
}

// directTransferAccounts are tryDirectTransfer's two accounts, as read
// (orig) and as written.
type directTransferAccounts struct {
	from, fromOrig, to, toOrig account.StateAccount
}

// flush adds the scratch's applied count to fastTransfers.
func (sc *fastTransferScratch) flush() {
	if sc.applied != 0 {
		fastTransfers.Add(sc.applied)
		sc.applied = 0
	}
	if sc.appliedDirect != 0 {
		directTransfers.Add(sc.appliedDirect)
		sc.appliedDirect = 0
	}
}

// transferShape holds a plain transfer's state-free facts, computed once by
// fastTransferShape for either fast path.
type transferShape struct {
	policy                     transitionChainPolicy
	rules                      *params.Rules
	from, to                   types.Address
	gasPrice, feeCap, tip, val *uint256.Int
	gas                        uint64
}

// fastTransferShape runs the fast path's checks that read no state and
// computes the gas*price debit and the balance the sender must hold into
// sc. ok is false when msg is not a plain transfer the fast path may apply.
func fastTransferShape(evm *vm2.EVM, msg *transaction.Message, sc *fastTransferScratch) (sh transferShape, ok bool) {
	// Shape checks: nothing here reads state.
	to := msg.To()
	if to == nil || len(msg.Data()) != 0 || len(msg.AccessList()) != 0 || len(msg.AuthList()) != 0 ||
		len(msg.BlobHashes()) != 0 || msg.IsFree() || !msg.CheckNonce() || msg.Gas() != params.TxGas {
		return sh, false
	}
	cfg := evm.Config()
	if cfg.Debug || cfg.RestoreState || dbgNonceBlock > 0 || dbgRefundBlock > 0 {
		return sh, false
	}
	rules := evm.ChainRules()
	// Amsterdam changes the intrinsic gas of a transfer (EIP-2780) and adds
	// a transfer log (EIP-7708): left to the EVM path.
	if rules.IsGlamsterdam {
		return sh, false
	}
	policy := newTransitionChainPolicy(evm.ChainConfig())
	if policy.shouldForceGasBailout() {
		return sh, false
	}
	from := msg.From()
	gasPrice, feeCap, tip, value := msg.ExecutionValues()
	gas := msg.Gas()
	intrinsic, err := IntrinsicGas(nil, nil, nil, false, rules.IsHomestead, rules.IsIstanbul, rules.IsShanghai, rules.IsPrague, false, !value.IsZero(), *to == from)
	if err != nil || intrinsic != gas {
		return sh, false
	}
	if rules.IsPrague && vm2.FloorDataGas(nil) > gas {
		return sh, false
	}
	if evm.IsPrecompile(*to) {
		return sh, false
	}
	blockCtx := evm.Context()
	if rules.IsLondon && (!cfg.NoBaseFee || !feeCap.IsZero() || !tip.IsZero()) {
		if CheckEip1559TxGasFeeCap(from, feeCap, tip, blockCtx.BaseFee, false) != nil {
			return sh, false
		}
	}
	mgval, balanceCheck := &sc.mgval, &sc.balanceCheck
	mgval.SetUint64(gas)
	if _, overflow := mgval.MulOverflow(mgval, gasPrice); overflow {
		return sh, false
	}
	balanceCheck.SetUint64(gas)
	if _, overflow := balanceCheck.MulOverflow(balanceCheck, feeCap); overflow {
		return sh, false
	}
	if _, overflow := balanceCheck.AddOverflow(balanceCheck, value); overflow {
		return sh, false
	}
	return transferShape{policy: policy, rules: rules, from: from, to: *to, gasPrice: gasPrice, feeCap: feeCap, tip: tip, val: value, gas: gas}, true
}

// creditTransferFees credits the priority fee and, where the chain collects
// it, the burnt base fee, exactly as TransitionDb computes them.
func creditTransferFees(evm *vm2.EVM, ibs *state.IntraBlockState, msg *transaction.Message, sink FeeSink, sh *transferShape, sc *fastTransferScratch) {
	blockCtx := evm.Context()
	effectiveTip := sh.gasPrice
	if sh.rules.IsLondon {
		if sh.feeCap.Gt(blockCtx.BaseFee) {
			effectiveTip = cmath.Min256(sh.tip, sc.tip.Sub(sh.feeCap, blockCtx.BaseFee))
		} else {
			effectiveTip = u256.Num0
		}
	}
	amount := sc.amount.SetUint64(sh.gas)
	amount.Mul(amount, effectiveTip)
	creditFeeTo(ibs, sink, sh.policy.priorityFeeRecipient(blockCtx.Coinbase), amount)
	if sh.policy.shouldCollectEIP1559Fee(msg, sh.rules) {
		burnAmount := sc.burn.SetUint64(sh.gas)
		burnAmount.Mul(burnAmount, blockCtx.BaseFee)
		creditFeeTo(ibs, sink, *evm.ChainConfig().Eip1559FeeCollector, burnAmount)
	}
}

// tryFastTransfer applies msg as a plain transfer when it is one. It returns
// (result, true) when the transaction was applied, and (nil, false) with no
// state written and gp untouched otherwise. sc may be nil; the result points
// into it and is valid until its next use.
func tryFastTransfer(evm *vm2.EVM, ibs *state.IntraBlockState, msg *transaction.Message, gp *common.GasPool, sink FeeSink, sc *fastTransferScratch) (*ExecutionResult, bool) {
	if sc == nil {
		sc = new(fastTransferScratch)
		defer sc.flush()
	}
	sh, ok := fastTransferShape(evm, msg, sc)
	if !ok {
		return nil, false
	}
	from, to, gas := sh.from, sh.to, sh.gas

	// State checks, in the EVM path's read order (preCheck: nonce, code
	// hash, balance; then the recipient).
	nonce := ibs.GetNonce(from)
	if nonce != msg.Nonce() || nonce+1 < nonce {
		return nil, false
	}
	if ch := ibs.GetCodeHash(from); ch != emptyCodeHash && ch != (types.Hash{}) {
		return nil, false // delegated (EIP-7702) or contract sender
	}
	if ibs.GetBalance(from).Lt(&sc.balanceCheck) {
		return nil, false
	}
	// A recipient that does not exist yet is created (or, for a zero value,
	// left untouched) by evm.Call: left to the EVM path.
	if !ibs.Exist(to) {
		return nil, false
	}
	if ch := ibs.GetCodeHash(to); ch != emptyCodeHash && ch != (types.Hash{}) {
		return nil, false
	}
	if gp.SubGas(gas) != nil {
		return nil, false
	}

	// Writes, in the EVM path's order: buyGas, the nonce bump, the value
	// transfer (CanTransfer holds: the balance covers gas*feeCap + value and
	// gas*gasPrice <= gas*feeCap was debited), the zero refund of refundGas.
	ibs.SubBalance(from, &sc.mgval)
	ibs.SetNonce(from, nonce+1)
	evm.Context().Transfer(ibs, from, to, sh.val, false)
	sc.zero.Clear()
	ibs.AddBalance(from, &sc.zero) // refundGas: no gas left, no refund counter
	gp.AddGas(0)

	creditTransferFees(evm, ibs, msg, sink, &sh, sc)
	sc.applied++
	sc.result = ExecutionResult{UsedGas: gas, BlockGasUsed: gas}
	return &sc.result, true
}

// normaliseLoaded is what IntraBlockState's newObject does to an account it
// loads: an uninitialised account reads as zero balance, a zero code hash
// as the empty code hash. original stays as read.
func normaliseLoaded(data *account.StateAccount) {
	if !data.Initialised {
		data.Balance.Clear()
		data.Initialised = true
	}
	if data.CodeHash == (types.Hash{}) {
		data.CodeHash = emptyCodeHash
	}
}

// tryDirectTransfer is tryFastTransfer without the IntraBlockState: the
// worker's Block-STM reader and writer are used directly. It records the
// same reads (sender, then recipient), the same writes (the dirty accounts
// in address order, through the writer's delta rule with the sender as the
// only balance-observed address), and the same balance-insensitivity marks
// as the IntraBlockState fast path followed by FinalizeTx and
// MarkBalanceInsensitive -- and nothing else, so the IntraBlockState and its
// per-transaction reset, journal, map work and FinalizeTx are skipped (about
// a third of the exec wave at 163k transfers).
//
// It applies only when the fees go to a sink (deferred), the value is
// non-zero and the recipient is not the sender, so that both accounts are
// non-empty afterwards (no EIP-161 removal) and the recipient's credit is a
// pure balance increase. Otherwise, or on any failed check, it returns
// false with rw's reads restored and nothing written; the caller then runs
// the IntraBlockState path, which records its reads afresh.
func tryDirectTransfer(evm *vm2.EVM, rd *parallel.ParallelStateReader, wr *parallel.ParallelStateWriter, rw *parallel.ReadWriteSet,
	msg *transaction.Message, gasLimit uint64, sink FeeSink, sc *fastTransferScratch) (*ExecutionResult, bool) {
	if sink == nil {
		return nil, false
	}
	sh, ok := fastTransferShape(evm, msg, sc)
	if !ok || sh.val.IsZero() || sh.from == sh.to || sh.gas > gasLimit {
		return nil, false
	}
	nReads := len(rw.Reads)
	fail := func() (*ExecutionResult, bool) {
		rw.Reads = rw.Reads[:nReads]
		return nil, false
	}

	// The sender (preCheck reads its nonce first).
	acc, err := rd.ReadAccountData(sh.from)
	if err != nil || acc == nil {
		return fail()
	}
	s := &sc.direct
	s.fromOrig = *acc // the reader's buffer is overwritten by the next read
	s.from = s.fromOrig
	normaliseLoaded(&s.from)
	nonce := s.from.Nonce
	if nonce != msg.Nonce() || nonce+1 < nonce {
		return fail()
	}
	if ch := s.from.CodeHash; ch != emptyCodeHash && ch != (types.Hash{}) {
		return fail()
	}
	if s.from.Balance.Lt(&sc.balanceCheck) {
		return fail()
	}
	// The recipient (evm.Call's Exist).
	acc, err = rd.ReadAccountData(sh.to)
	if err != nil || acc == nil {
		return fail()
	}
	s.toOrig = *acc
	s.to = s.toOrig
	normaliseLoaded(&s.to)
	if ch := s.to.CodeHash; ch != emptyCodeHash && ch != (types.Hash{}) {
		return fail()
	}
	// buyGas, the nonce bump, the transfer, the zero refund.
	s.from.Balance.Sub(&s.from.Balance, &sc.mgval)
	s.from.Nonce = nonce + 1
	s.from.Balance.Sub(&s.from.Balance, sh.val)
	s.to.Balance.Add(&s.to.Balance, sh.val)
	if s.to.Balance.IsZero() {
		return fail() // wrapped: leave it to the EVM path's semantics
	}

	// FinalizeTx: the dirty accounts in address order.
	if bytes.Compare(sh.from[:], sh.to[:]) < 0 {
		wr.UpdateAccountDataEligible(sh.from, &s.fromOrig, &s.from, false)
		wr.UpdateAccountDataEligible(sh.to, &s.toOrig, &s.to, true)
	} else {
		wr.UpdateAccountDataEligible(sh.to, &s.toOrig, &s.to, true)
		wr.UpdateAccountDataEligible(sh.from, &s.fromOrig, &s.from, false)
	}
	from := sh.from
	rw.MarkBalanceInsensitive(func(a types.Address) bool { return a == from })

	creditTransferFees(evm, nil, msg, sink, &sh, sc)
	sc.applied++
	sc.appliedDirect++
	sc.result = ExecutionResult{UsedGas: sh.gas, BlockGasUsed: sh.gas}
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
