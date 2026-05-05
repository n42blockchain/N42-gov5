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
// StateTransition: per-transaction execution driver. Handles nonce
// and balance checks, buys gas, creates or calls the target
// contract through the EVM, refunds unused gas and commits or
// reverts the IntraBlockState accordingly. Central entry point
// invoked by ApplyTransaction.

package internal

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	cmath "github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

/*
The State Transitioning Model

A state transition is a change made when a transaction is applied to the current world state.
The state transitioning model does all the necessary work to work out a valid new state root.

1) Nonce handling
2) Pre pay gas
3) Create a new state object if the recipient is \0*32
4) Value transfer
== If contract creation ==

	4a) Attempt to run transaction data
	4b) If valid, use result as code for the new state object

== end ==
5) Run Script section
6) Derive new state root
*/
type StateTransition struct {
	gp         *common.GasPool
	msg        Message
	gas        uint64
	gasPrice   *uint256.Int
	gasFeeCap  *uint256.Int
	tip        *uint256.Int
	initialGas uint64
	value      *uint256.Int
	data       []byte
	state      evmtypes.IntraBlockState
	evm        vm2.VMInterface

	sharedBuyGas        *uint256.Int
	sharedBuyGasBalance *uint256.Int

	policy transitionChainPolicy
}

type transitionChainPolicy struct {
	forceGasBailout            bool
	priorityFeeToSystemAddress bool
}

func newTransitionChainPolicy(cfg *params.ChainConfig) transitionChainPolicy {
	if cfg == nil {
		return transitionChainPolicy{}
	}
	parliaSemantics := cfg.UsesParliaRules()
	return transitionChainPolicy{
		forceGasBailout:            parliaSemantics,
		priorityFeeToSystemAddress: parliaSemantics,
	}
}

func (p transitionChainPolicy) shouldForceGasBailout() bool {
	return p.forceGasBailout
}

func (p transitionChainPolicy) priorityFeeRecipient(coinbase types.Address) types.Address {
	if p.priorityFeeToSystemAddress {
		return consensus.SystemAddress
	}
	return coinbase
}

func (p transitionChainPolicy) shouldCollectEIP1559Fee(msg Message, rules *params.Rules) bool {
	return rules != nil && !msg.IsFree() && rules.IsLondon && rules.IsEip1559FeeCollector
}

// Message represents a message sent to a contract.
type Message interface {
	From() types.Address
	To() *types.Address

	GasPrice() *uint256.Int
	FeeCap() *uint256.Int
	Tip() *uint256.Int
	BlobFeeCap() *uint256.Int
	BlobHashes() []types.Hash
	Gas() uint64
	Value() *uint256.Int

	Nonce() uint64
	CheckNonce() bool
	Data() []byte
	AccessList() transaction.AccessList
	AuthList() transaction.AuthorizationList // EIP-7702

	IsFree() bool
}

// ExecutionResult includes all output after executing given evm message
// no matter the execution itself is successful or not.
type ExecutionResult struct {
	UsedGas    uint64 // Total used gas including refunded gas
	Err        error  // Any error encountered during execution
	ReturnData []byte // Returned data from evm
}

// Unwrap returns the internal evm error for further analysis.
func (result *ExecutionResult) Unwrap() error {
	return result.Err
}

// Failed returns whether the execution failed.
func (result *ExecutionResult) Failed() bool { return result.Err != nil }

// Return returns the data after execution if no error occurs.
func (result *ExecutionResult) Return() []byte {
	if result.Err != nil {
		return nil
	}
	return types.CopyBytes(result.ReturnData)
}

// Revert returns the concrete revert reason if the execution is aborted by REVERT opcode.
func (result *ExecutionResult) Revert() []byte {
	if result.Err != vm2.ErrExecutionReverted {
		return nil
	}
	return types.CopyBytes(result.ReturnData)
}

// IntrinsicGas computes the 'intrinsic gas' for a message with the given data.
func IntrinsicGas(data []byte, accessList transaction.AccessList, authList transaction.AuthorizationList, isContractCreation bool, isHomestead, isEIP2028 bool, isEIP3860 bool, isPrague bool, isGlamsterdam bool) (uint64, error) {
	var gas uint64
	if isContractCreation && isHomestead {
		if isGlamsterdam {
			gas = params.TxGasContractCreationGlamsterdam
		} else {
			gas = params.TxGasContractCreation
		}
	} else {
		if isGlamsterdam {
			gas = params.TxGasGlamsterdam
		} else {
			gas = params.TxGas
		}
	}

	dataLen := uint64(len(data))
	if dataLen > 0 {
		var nz uint64
		for _, byt := range data {
			if byt != 0 {
				nz++
			}
		}

		nonZeroGas := params.TxDataNonZeroGasFrontier
		if isGlamsterdam {
			nonZeroGas = params.TxDataNonZeroGasGlamsterdam
		} else if isEIP2028 {
			nonZeroGas = params.TxDataNonZeroGasEIP2028
		}
		if (math.MaxUint64-gas)/nonZeroGas < nz {
			return 0, ErrGasUintOverflow
		}
		gas += nz * nonZeroGas

		zeroGas := params.TxDataZeroGas
		if isGlamsterdam {
			zeroGas = params.TxDataZeroGasGlamsterdam
		}
		z := dataLen - nz
		if (math.MaxUint64-gas)/zeroGas < z {
			return 0, ErrGasUintOverflow
		}
		gas += z * zeroGas

		if isContractCreation && isEIP3860 {
			lenWords := toWordSize(dataLen)
			if (math.MaxUint64-gas)/params.InitCodeWordGas < lenWords {
				return 0, ErrGasUintOverflow
			}
			gas += lenWords * params.InitCodeWordGas
		}
	}

	if accessList != nil {
		accessListAddressGas := params.TxAccessListAddressGas
		accessListStorageKeyGas := params.TxAccessListStorageKeyGas
		if isGlamsterdam {
			accessListAddressGas = params.TxAccessListAddressGasGlamsterdam
			accessListStorageKeyGas = params.TxAccessListStorageKeyGasGlamsterdam
		}
		gas += uint64(len(accessList)) * accessListAddressGas
		gas += uint64(accessList.StorageKeys()) * accessListStorageKeyGas
	}
	if isPrague && len(authList) > 0 {
		if (math.MaxUint64-gas)/params.PerEmptyAccountCost < uint64(len(authList)) {
			return 0, ErrGasUintOverflow
		}
		gas += uint64(len(authList)) * params.PerEmptyAccountCost
	}
	return gas, nil
}

var stateTransitionPool = sync.Pool{
	New: func() any {
		return &StateTransition{
			sharedBuyGas:        new(uint256.Int),
			sharedBuyGasBalance: new(uint256.Int),
		}
	},
}

// NewStateTransition initialises and returns a new state transition object.
func NewStateTransition(evm vm2.VMInterface, msg Message, gp *common.GasPool) *StateTransition {
	st := stateTransitionPool.Get().(*StateTransition)
	st.gp = gp
	st.evm = evm
	st.msg = msg
	st.gas = 0
	st.gasPrice = msg.GasPrice()
	st.gasFeeCap = msg.FeeCap()
	st.tip = msg.Tip()
	st.initialGas = 0
	st.value = msg.Value()
	st.data = msg.Data()
	st.state = evm.IntraBlockState()
	st.sharedBuyGas.Clear()
	st.sharedBuyGasBalance.Clear()
	st.policy = newTransitionChainPolicy(evm.ChainConfig())
	return st
}

// release returns the StateTransition to the pool. Safe only after
// TransitionDb has fully completed (no remaining references).
func (st *StateTransition) release() {
	st.gp = nil
	st.evm = nil
	st.msg = nil
	st.gasPrice = nil
	st.gasFeeCap = nil
	st.tip = nil
	st.value = nil
	st.data = nil
	st.state = nil
	stateTransitionPool.Put(st)
}

var dbgRefundBlock = func() uint64 {
	v := os.Getenv("DBG_SSTORE")
	if v == "" {
		return 0
	}
	n, _ := strconv.ParseUint(v, 10, 64)
	return n
}()

// ApplyMessage computes the new state by applying the given message against the old state.
// Returns the execution result and an error if the message would always fail for that state.
// `refunds` is false when gas refunds should not be applied.
// `gasBailout` is true when the transaction should not fail if balance is insufficient for gas.
func ApplyMessage(evm vm2.VMInterface, msg Message, gp *common.GasPool, refunds bool, gasBailout bool) (*ExecutionResult, error) {
	st := NewStateTransition(evm, msg, gp)
	res, err := st.TransitionDb(refunds, gasBailout)
	st.release()
	return res, err
}

func (st *StateTransition) to() types.Address {
	if st.msg == nil || st.msg.To() == nil {
		return types.Address{}
	}
	return *st.msg.To()
}

func (st *StateTransition) buyGas(gasBailout bool) error {
	mgval := st.sharedBuyGas
	mgval.SetUint64(st.msg.Gas())
	mgval, overflow := mgval.MulOverflow(mgval, st.gasPrice)
	if overflow {
		return fmt.Errorf("%w: address %v", ErrInsufficientFunds, st.msg.From().Hex())
	}

	balanceCheck := mgval
	if st.gasFeeCap != nil {
		balanceCheck = st.sharedBuyGasBalance.SetUint64(st.msg.Gas())
		balanceCheck, overflow = balanceCheck.MulOverflow(balanceCheck, st.gasFeeCap)
		if overflow {
			return fmt.Errorf("%w: address %v", ErrInsufficientFunds, st.msg.From().Hex())
		}
		balanceCheck, overflow = balanceCheck.AddOverflow(balanceCheck, st.value)
		if overflow {
			return fmt.Errorf("%w: address %v", ErrInsufficientFunds, st.msg.From().Hex())
		}
	}
	blobBalanceCheck, blobFee := st.blobFeeCost(balanceCheck)
	if blobBalanceCheck != nil {
		balanceCheck = blobBalanceCheck
	}

	var subBalance bool
	if have, want := st.state.GetBalance(st.msg.From()), balanceCheck; have.Cmp(want) < 0 {
		if !gasBailout {
			return fmt.Errorf("%w: address %v have %v want %v", ErrInsufficientFunds, st.msg.From().Hex(), have, want)
		}
	} else {
		subBalance = true
	}

	if err := st.gp.SubGas(st.msg.Gas()); err != nil {
		if !gasBailout {
			return err
		}
	}
	st.gas += st.msg.Gas()
	st.initialGas = st.msg.Gas()

	if subBalance {
		st.state.SubBalance(st.msg.From(), mgval)
		if blobFee != nil {
			st.state.SubBalance(st.msg.From(), blobFee)
		}
	}
	return nil
}

// CheckEip1559TxGasFeeCap validates that the gas fee cap and tip conform to EIP-1559 rules.
func CheckEip1559TxGasFeeCap(from types.Address, gasFeeCap, tip, baseFee *uint256.Int, isFree bool) error {
	if gasFeeCap.Lt(tip) {
		return fmt.Errorf("%w: address %v, tip: %s, gasFeeCap: %s", ErrTipAboveFeeCap,
			from.Hex(), tip, gasFeeCap)
	}
	if baseFee != nil && gasFeeCap.Lt(baseFee) && !isFree {
		return fmt.Errorf("%w: address %v, gasFeeCap: %s baseFee: %s", ErrFeeCapTooLow,
			from.Hex(), gasFeeCap, baseFee)
	}
	return nil
}

func (st *StateTransition) preCheck(gasBailout bool) error {
	if st.msg.CheckNonce() {
		stNonce := st.state.GetNonce(st.msg.From())
		if msgNonce := st.msg.Nonce(); stNonce < msgNonce {
			return fmt.Errorf("%w: address %v, tx: %d state: %d", ErrNonceTooHigh,
				st.msg.From().Hex(), msgNonce, stNonce)
		} else if stNonce > msgNonce {
			return fmt.Errorf("%w: address %v, tx: %d state: %d", ErrNonceTooLow,
				st.msg.From().Hex(), msgNonce, stNonce)
		} else if stNonce+1 < stNonce {
			return fmt.Errorf("%w: address %v, nonce: %d", ErrNonceMax,
				st.msg.From().Hex(), stNonce)
		}

		// EIP-3607: Reject transactions from senders with deployed code.
		// EIP-7702 exception: accounts with delegation code (0xef0100 prefix) are still valid EOA senders.
		if codeHash := st.state.GetCodeHash(st.msg.From()); codeHash != emptyCodeHash && codeHash != (types.Hash{}) {
			code := st.state.GetCode(st.msg.From())
			if !vm2.HasDelegation(code) {
				return fmt.Errorf("%w: address %v, codehash: %s", ErrSenderNoEOA,
					st.msg.From().Hex(), codeHash)
			}
		}
	}

	// EIP-1559: Validate gas fee cap against block base fee
	if st.evm.ChainRules().IsLondon {
		if !st.evm.Config().NoBaseFee || !st.gasFeeCap.IsZero() || !st.tip.IsZero() {
			if err := CheckEip1559TxGasFeeCap(st.msg.From(), st.gasFeeCap, st.tip, st.evm.Context().BaseFee, st.msg.IsFree()); err != nil {
				return err
			}
		}
	}
	if len(st.msg.BlobHashes()) > 0 {
		blobFeeCap := st.msg.BlobFeeCap()
		currentBlobFee := st.currentBlobBaseFee()
		if blobFeeCap == nil || currentBlobFee == nil || blobFeeCap.Lt(currentBlobFee) {
			return fmt.Errorf("%w: address %v, blobFeeCap: %s blobBaseFee: %s", transaction.ErrBlobFeeCapTooLow,
				st.msg.From().Hex(), formatUint256(blobFeeCap), formatUint256(currentBlobFee))
		}
	}
	return st.buyGas(gasBailout)
}

// TransitionDb transitions the state by applying the current message and
// returning the evm execution result.
func (st *StateTransition) TransitionDb(refunds bool, gasBailout bool) (*ExecutionResult, error) {
	// BSC/Parlia always uses gas bailout for system transactions
	if st.policy.shouldForceGasBailout() {
		gasBailout = true
	}

	if err := st.preCheck(gasBailout); err != nil {
		return nil, err
	}

	if st.evm.Config().Debug {
		st.evm.Config().Tracer.CaptureTxStart(st.initialGas)
		defer func() {
			st.evm.Config().Tracer.CaptureTxEnd(st.gas)
		}()
	}

	msg := st.msg
	sender := vm2.AccountRef(msg.From())
	contractCreation := msg.To() == nil
	rules := st.evm.ChainRules()

	// Check intrinsic gas
	gas, err := IntrinsicGas(st.data, st.msg.AccessList(), st.msg.AuthList(), contractCreation, rules.IsHomestead, rules.IsIstanbul, rules.IsShanghai, rules.IsPrague, rules.IsGlamsterdam)
	if err != nil {
		return nil, err
	}
	if st.gas < gas {
		return nil, fmt.Errorf("%w: have %d, want %d", ErrIntrinsicGas, st.gas, gas)
	}
	st.gas -= gas

	// EIP-7623: Floor data gas for Prague/Pectra
	var floorDataGas uint64
	if rules.IsPrague {
		floorDataGas = vm2.FloorDataGas(st.data, rules.IsGlamsterdam)
		if st.initialGas < floorDataGas {
			return nil, fmt.Errorf("%w: have %d, want %d", ErrIntrinsicGas, st.initialGas, floorDataGas)
		}
	}

	var bailout bool
	if gasBailout {
		if !msg.Value().IsZero() && !st.evm.Context().CanTransfer(st.state, msg.From(), msg.Value()) {
			bailout = true
		}
	}

	// Set up access list (EIP-2929)
	if rules.IsBerlin {
		st.state.PrepareAccessList(msg.From(), msg.To(), vm2.ActivePrecompiles(rules), msg.AccessList())
		// EIP-3651: Warm COINBASE
		if rules.IsShanghai {
			st.state.AddAddressToAccessList(st.evm.Context().Coinbase)
		}
	}

	var (
		ret   []byte
		vmerr error
	)
	if contractCreation {
		ret, _, st.gas, vmerr = st.evm.Create(sender, st.data, st.gas, st.value)
	} else {
		st.state.SetNonce(msg.From(), st.state.GetNonce(sender.Address())+1)
		if rules.IsPrague && msg.AuthList() != nil {
			if err := st.applyAuthorizations(msg.AuthList()); err != nil {
				return nil, err
			}
		}
		if rules.IsPrague {
			if delegatedAddr, ok := vm2.ParseDelegation(st.state.GetCode(st.to())); ok {
				st.state.AddAddressToAccessList(delegatedAddr)
			}
		}
		ret, st.gas, vmerr = st.evm.Call(sender, st.to(), st.data, st.gas, st.value, bailout)
	}

	if refunds {
		// Dump refund state when DBG_SSTORE targets this block.
		if dbgRefundBlock > 0 && st.evm.Context().BlockNumber == dbgRefundBlock {
			if ti, ok := st.state.(interface{ TxIndex() int }); ok {
				fmt.Fprintf(os.Stderr, "REFUND bn=%d ti=%d gasUsed=%d refund=%d\n",
					st.evm.Context().BlockNumber, ti.TxIndex(), st.gasUsed(), st.state.GetRefund())
			}
		}
		if rules.IsLondon {
			st.refundGas(params.RefundQuotientEIP3529, floorDataGas)
		} else {
			st.refundGas(params.RefundQuotient, floorDataGas)
		}
	} else if rules.IsPrague && floorDataGas > 0 {
		if gasUsed := st.gasUsed(); gasUsed < floorDataGas {
			st.gas = st.initialGas - floorDataGas
		}
	}

	// Calculate effective tip and distribute to block producer
	effectiveTip := st.gasPrice
	if rules.IsLondon {
		if st.gasFeeCap.Gt(st.evm.Context().BaseFee) {
			effectiveTip = cmath.Min256(st.tip, new(uint256.Int).Sub(st.gasFeeCap, st.evm.Context().BaseFee))
		} else {
			effectiveTip = u256.Num0
		}
	}

	amount := new(uint256.Int).SetUint64(st.gasUsed())
	amount.Mul(amount, effectiveTip)
	st.state.AddBalance(st.policy.priorityFeeRecipient(st.evm.Context().Coinbase), amount)

	// EIP-1559 fee collection
	if st.policy.shouldCollectEIP1559Fee(msg, rules) {
		burntContractAddress := *st.evm.ChainConfig().Eip1559FeeCollector
		burnAmount := new(uint256.Int).Mul(new(uint256.Int).SetUint64(st.gasUsed()), st.evm.Context().BaseFee)
		st.state.AddBalance(burntContractAddress, burnAmount)
	}

	return &ExecutionResult{
		UsedGas:    st.gasUsed(),
		Err:        vmerr,
		ReturnData: ret,
	}, nil
}

func (st *StateTransition) refundGas(refundQuotient uint64, floorDataGas uint64) {
	refund := st.gasUsed() / refundQuotient
	if refund > st.state.GetRefund() {
		refund = st.state.GetRefund()
	}
	st.gas += refund
	if floorDataGas > 0 {
		maxRemainingGas := st.initialGas - floorDataGas
		if st.gas > maxRemainingGas {
			st.gas = maxRemainingGas
		}
	}

	remaining := new(uint256.Int).Mul(new(uint256.Int).SetUint64(st.gas), st.gasPrice)
	st.state.AddBalance(st.msg.From(), remaining)
	st.gp.AddGas(st.gas)
}

func (st *StateTransition) gasUsed() uint64 {
	return st.initialGas - st.gas
}

func (st *StateTransition) currentBlobBaseFee() *uint256.Int {
	if len(st.msg.BlobHashes()) == 0 {
		return nil
	}
	if cfg := st.evm.ChainConfig(); cfg != nil {
		return cfg.CalcBlobFee(st.evm.Context().ExcessBlobGas, st.evm.Context().Time)
	}
	if st.evm.Context().BlobBaseFee == nil {
		return nil
	}
	return new(uint256.Int).Set(st.evm.Context().BlobBaseFee)
}

func (st *StateTransition) blobFeeCost(balanceCheck *uint256.Int) (*uint256.Int, *uint256.Int) {
	if len(st.msg.BlobHashes()) == 0 {
		return nil, nil
	}
	blobGas := uint64(len(st.msg.BlobHashes())) * params.BlobTxBlobGasPerBlob
	blobFeeCap := st.msg.BlobFeeCap()
	currentBlobFee := st.currentBlobBaseFee()
	if blobFeeCap == nil || currentBlobFee == nil {
		return nil, nil
	}
	maxBlobCost := new(uint256.Int).SetUint64(blobGas)
	maxBlobCost.Mul(maxBlobCost, blobFeeCap)
	balanceCheck = new(uint256.Int).Add(balanceCheck, maxBlobCost)

	actualBlobCost := new(uint256.Int).SetUint64(blobGas)
	actualBlobCost.Mul(actualBlobCost, currentBlobFee)
	return balanceCheck, actualBlobCost
}

func formatUint256(v *uint256.Int) string {
	if v == nil {
		return "<nil>"
	}
	return v.String()
}

// toWordSize returns the ceiled word size required for init code payment calculation.
func toWordSize(size uint64) uint64 {
	if size > math.MaxUint64-31 {
		return math.MaxUint64/32 + 1
	}
	return (size + 31) / 32
}

// applyAuthorizations processes the EIP-7702 authorization list.
// For each valid authorization, it sets delegation code on the signer's account.
func (st *StateTransition) applyAuthorizations(authList transaction.AuthorizationList) error {
	for _, auth := range authList {
		if auth == nil {
			continue
		}

		// Chain ID must match or be 0 (wildcard)
		if !auth.ChainID.IsZero() && auth.ChainID.CmpBig(st.evm.ChainConfig().ChainID) != 0 {
			continue
		}
		// Nonce must remain within EIP-2681 bounds.
		if auth.Nonce+1 < auth.Nonce {
			continue
		}

		signer, err := auth.RecoverSigner()
		if err != nil {
			continue
		}
		st.state.AddAddressToAccessList(signer)

		// Nonce must match
		signerNonce := st.state.GetNonce(signer)
		if auth.Nonce != signerNonce {
			continue
		}

		wasEmpty := st.state.Empty(signer)

		// Account must not have non-delegation code
		existingCode := st.state.GetCode(signer)
		if len(existingCode) > 0 && !vm2.HasDelegation(existingCode) {
			continue
		}

		if !wasEmpty && params.PerEmptyAccountCost > params.PerAuthBaseCost {
			st.state.AddRefund(params.PerEmptyAccountCost - params.PerAuthBaseCost)
		}
		st.state.SetNonce(signer, signerNonce+1)
		if auth.Address == (types.Address{}) {
			st.state.SetCode(signer, nil)
		} else {
			st.state.SetCode(signer, vm2.AddressToDelegation(auth.Address))
		}
	}

	return nil
}
