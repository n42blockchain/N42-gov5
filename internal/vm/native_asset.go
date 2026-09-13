// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// native_asset.go — the native asset module: USDT transfer(address,uint256)
// executed without the interpreter.
//
// Gated by ChainConfig.NativeAssetTime (Rules.IsNativeAsset), which no Ethereum
// chainspec sets. The module is bytecode-equivalent by design: every call it
// accepts leaves exactly the state, logs, refund counter, access list,
// remaining gas and state-read order the TetherToken bytecode (code hash
// b44fb4e9...ea55) would, so enabling it changes no consensus result. Anything
// outside the one shape it models goes to the interpreter:
//
//   - a CALL (not CALLCODE, DELEGATECALL or STATICCALL) outside a static
//     context, with no tracer, zero value, and exactly 68 bytes of calldata
//     carrying the transfer selector and a clean address word
//   - not paused, not deprecated, sender not blacklisted, basisPointsRate == 0
//     (so the fee is zero and there is exactly one Transfer event)
//   - sender balance >= amount and no overflow on the recipient
//   - enough gas to finish: the interpreter would not run out of gas or trip
//     the SSTORE sentry anywhere on the path
//   - London through Osaka pricing (EIP-2929 + EIP-3529); Amsterdam reprices
//     SSTORE and is excluded
//
// Read ORDER is part of the equivalence, not just the read set. A witness
// stream (internal/ethel/witness.go) is positional: one [len][value] entry per
// read against the underlying state reader, in execution order, with no keys.
// A replay serves reads by position, so one read out of place shifts every
// value after it. Two rules follow:
//
//   - the module loads slots in exactly the order the bytecode first loads
//     them, including maximumFee, whose value it never needs
//   - on a hand-back its loads are a prefix of the interpreter's, whose
//     re-reads then hit the state cache instead of the stream. That covers
//     gas too: before each load the plan charges the gas the interpreter
//     would have spent reaching it, and stops before reading anything the
//     interpreter would not reach because it ran out of gas first.
//
// Gas is the bytecode's static cost split at its nine storage accesses
// (usdtSegments, measured per opcode with a tracer) plus SLOAD/SSTORE charges
// computed as operations_acl.go computes them. TestNativeAssetGasSegments pins
// the segments; TestNativeAssetGasSweep compares both paths at every gas value.
//
// NativeAssetShadow runs both paths on every accepted call, compares their
// effects through a recording state wrapper, and keeps the interpreter's
// result. That is the mode the full-year differential test runs in.

package vm

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

var (
	usdtAddress        = types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	usdtCodeHash       = types.HexToHash("0xb44fb4e949d0f78f87f79ee46428f23a2a5713ce6fc6e0beb3dda78c2ac1ea55")
	erc20TransferTopic = types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	usdtTransferSel    = [4]byte{0xa9, 0x05, 0x9c, 0xbb}
)

// TetherToken storage layout (solc 0.4.18, inheritance linearised as Ownable,
// Pausable, ERC20Basic, BasicToken, StandardToken, BlackList, TetherToken).
const (
	usdtSlotOwnerPaused = 0  // owner (low 160 bits) | paused (bool at bit 160)
	usdtSlotBalances    = 2  // mapping(address => uint) balances
	usdtSlotFeeRate     = 3  // basisPointsRate
	usdtSlotMaxFee      = 4  // maximumFee
	usdtSlotBlacklist   = 6  // mapping(address => bool) isBlackListed
	usdtSlotUpgraded    = 10 // upgradedAddress (low 160 bits) | deprecated (bool at bit 160)
)

// usdtSegments[i] is the static gas -- opcodes whose cost does not depend on
// state -- the transfer path spends before its i-th storage access. The
// accesses, in order: SLOAD paused, SLOAD isBlackListed[sender], SLOAD
// deprecated, SLOAD basisPointsRate, SLOAD maximumFee, SLOAD balances[sender],
// SSTORE balances[sender], SLOAD balances[to], SSTORE balances[to]. The last
// entry is everything after the final SSTORE (the Transfer event, memory and
// the return). They sum to 4001.
var usdtSegments = [10]uint64{654, 201, 62, 184, 277, 230, 193, 101, 207, 1892}

// usdtSegment4Zero replaces segment 4 for a zero amount: SafeMath.mul returns
// early before maximumFee is loaded. The zero-amount path sums to 3935.
const usdtSegment4Zero uint64 = 211

// USDTTransferGasSegments returns the modeled path's static gas segments, for
// the test that pins them against the bytecode.
func USDTTransferGasSegments(zeroAmount bool) [10]uint64 {
	s := usdtSegments
	if zeroAmount {
		s[4] = usdtSegment4Zero
	}
	return s
}

// NativeAssetShadow makes every accepted call run through both the module and
// the interpreter, compare, and keep the interpreter's result. Set before any
// EVM runs.
var NativeAssetShadow bool

// NativeAssetStatsEnabled turns on the process-wide counters. Off by default:
// one shared atomic per USDT call is measurable contention at high worker
// counts. Shadow mismatches are always counted.
var NativeAssetStatsEnabled bool

// NativeAssetStats counts what the module did with the calls it was offered.
type NativeAssetStats struct {
	Native          atomic.Uint64 // executed natively (not shadow)
	ShadowMatched   atomic.Uint64 // shadow: module and interpreter agreed
	ShadowMismatch  atomic.Uint64 // shadow: they did not (always counted)
	FallbackShape   atomic.Uint64 // not a plain CALL to transfer with clean calldata
	FallbackGuard   atomic.Uint64 // paused, blacklisted, deprecated or fee enabled
	FallbackBalance atomic.Uint64 // insufficient balance or recipient overflow
	FallbackGas     atomic.Uint64 // the interpreter would run out of gas on the path
}

// NativeAssetCounters is the process-wide instance.
var NativeAssetCounters NativeAssetStats

func countNativeAsset(c *atomic.Uint64) {
	if NativeAssetStatsEnabled {
		c.Add(1)
	}
}

func usdtSlot(n uint64) types.Hash {
	var h types.Hash
	h[31] = byte(n)
	return h
}

func usdtMappingKey(addr types.Address, slot uint64) types.Hash {
	var buf [64]byte
	copy(buf[12:32], addr[:])
	buf[63] = byte(slot)
	return crypto.Keccak256Hash(buf[:])
}

type usdtPlan struct {
	sender, to   types.Address
	amount       uint256.Int
	keyBlacklist types.Hash
	keySender    types.Hash
	keyTo        types.Hash
	balSender    uint256.Int
	balTo        uint256.Int // balances[to] as loaded, before the sender's write
	segments     [10]uint64
	gas          uint64
}

// usdtWarm tracks which of the transfer's slots are warm, consulting the
// access list the first time each slot is touched. The path loads at most
// seven distinct slots.
type usdtWarm struct {
	ibs   evmtypes.IntraBlockState
	slots [7]types.Hash
	n     int
}

// sload returns the SLOAD charge for slot and marks it warm, adding it to the
// access list when apply.
func (w *usdtWarm) sload(slot types.Hash, apply bool) uint64 {
	for i := 0; i < w.n; i++ {
		if w.slots[i] == slot {
			return params.WarmStorageReadCostEIP2929
		}
	}
	if w.n < len(w.slots) {
		w.slots[w.n] = slot
		w.n++
	}
	if _, present := w.ibs.SlotInAccessList(usdtAddress, slot); present {
		return params.WarmStorageReadCostEIP2929
	}
	if apply {
		w.ibs.AddSlotToAccessList(usdtAddress, slot)
	}
	return params.ColdSloadCostEIP2929
}

// usdtSStore prices an SSTORE to a slot the path has just loaded -- so it is
// warm -- exactly as makeGasSStoreFunc(SstoreClearsScheduleRefundEIP3529)
// does, and applies its refund-counter changes when apply.
func usdtSStore(ibs evmtypes.IntraBlockState, slot types.Hash, current, next *uint256.Int, apply bool) uint64 {
	if current.Eq(next) {
		return params.WarmStorageReadCostEIP2929
	}
	clearing := params.SstoreClearsScheduleRefundEIP3529
	var original uint256.Int
	ibs.GetCommittedState(usdtAddress, &slot, &original)
	if original.Eq(current) {
		if original.IsZero() {
			return params.SstoreSetGasEIP2200
		}
		if next.IsZero() && apply {
			ibs.AddRefund(clearing)
		}
		return params.SstoreResetGasEIP2200 - params.ColdSloadCostEIP2929
	}
	if !original.IsZero() && apply {
		if current.IsZero() {
			ibs.SubRefund(clearing)
		} else if next.IsZero() {
			ibs.AddRefund(clearing)
		}
	}
	if original.Eq(next) && apply {
		if original.IsZero() {
			ibs.AddRefund(params.SstoreSetGasEIP2200 - params.WarmStorageReadCostEIP2929)
		} else {
			ibs.AddRefund((params.SstoreResetGasEIP2200 - params.ColdSloadCostEIP2929) - params.WarmStorageReadCostEIP2929)
		}
	}
	return params.WarmStorageReadCostEIP2929
}

// callNativeAsset runs a CALL whose target is the USDT address. It either
// executes the transfer natively or hands the call to the interpreter.
func (evm *EVM) callNativeAsset(c *Contract, input []byte, value *uint256.Int) ([]byte, error) {
	var p usdtPlan
	if !evm.planUSDTTransfer(c, input, value, &p) {
		return run(evm, c, input, false)
	}
	if NativeAssetShadow {
		return evm.shadowUSDTTransfer(c, input, &p)
	}
	evm.applyUSDTTransfer(c, &p)
	countNativeAsset(&NativeAssetCounters.Native)
	return nil, nil
}

// planUSDTTransfer decides whether the call is in the modeled shape. It walks
// the path in bytecode order, charging gas as the interpreter would and loading
// each slot only once the interpreter would have reached it, and changes no
// state. On false, everything it loaded is a prefix of what the interpreter
// will load.
func (evm *EVM) planUSDTTransfer(c *Contract, input []byte, value *uint256.Int, p *usdtPlan) bool {
	rules := evm.chainRules
	if !rules.IsLondon || rules.IsGlamsterdam || evm.config.Debug || c.CodeHash != usdtCodeHash ||
		(value != nil && !value.IsZero()) || len(input) != 68 || [4]byte(input[:4]) != usdtTransferSel {
		countNativeAsset(&NativeAssetCounters.FallbackShape)
		return false
	}
	if in, ok := evm.interpreter.(*EVMInterpreter); !ok || in.readOnly {
		countNativeAsset(&NativeAssetCounters.FallbackShape)
		return false
	}
	for _, b := range input[4:16] {
		if b != 0 {
			countNativeAsset(&NativeAssetCounters.FallbackShape)
			return false
		}
	}
	p.sender = c.CallerAddress
	copy(p.to[:], input[16:36])
	p.amount.SetBytes(input[36:68])
	p.segments = USDTTransferGasSegments(p.amount.IsZero())
	p.keyBlacklist = usdtMappingKey(p.sender, usdtSlotBlacklist)
	p.keySender = usdtMappingKey(p.sender, usdtSlotBalances)
	p.keyTo = usdtMappingKey(p.to, usdtSlotBalances)

	ibs := evm.intraBlockState
	warm := usdtWarm{ibs: ibs}
	left := c.Gas
	charge := func(n uint64) bool {
		if left < n {
			return false
		}
		left -= n
		return true
	}
	outOfGas := func() bool {
		countNativeAsset(&NativeAssetCounters.FallbackGas)
		return false
	}
	guard := func() bool {
		countNativeAsset(&NativeAssetCounters.FallbackGuard)
		return false
	}
	// load reaches storage access i and reads slot, or reports that the
	// interpreter runs out of gas first and so never reads it.
	load := func(i int, slot types.Hash, out *uint256.Int) bool {
		if !charge(p.segments[i]) || !charge(warm.sload(slot, false)) {
			return false
		}
		ibs.GetState(usdtAddress, &slot, out)
		return true
	}
	// sstore reaches storage access i and prices it, including the sentry.
	sstore := func(i int, slot types.Hash, current, next *uint256.Int) bool {
		return charge(p.segments[i]) && left > params.SstoreSentryGasEIP2200 &&
			charge(usdtSStore(ibs, slot, current, next, false))
	}

	var w, hi uint256.Int
	if !load(0, usdtSlot(usdtSlotOwnerPaused), &w) {
		return outOfGas()
	}
	if !hi.Rsh(&w, 160).IsZero() {
		return guard()
	}
	if !load(1, p.keyBlacklist, &w) {
		return outOfGas()
	}
	if !w.IsZero() {
		return guard()
	}
	if !load(2, usdtSlot(usdtSlotUpgraded), &w) {
		return outOfGas()
	}
	if !hi.Rsh(&w, 160).IsZero() {
		return guard()
	}
	if !load(3, usdtSlot(usdtSlotFeeRate), &w) {
		return outOfGas()
	}
	if !w.IsZero() {
		return guard()
	}
	// `if (fee > maximumFee)` loads maximumFee. The value cannot matter with a
	// zero fee; the load is part of the witness order.
	if !load(4, usdtSlot(usdtSlotMaxFee), &w) {
		return outOfGas()
	}
	if !load(5, p.keySender, &p.balSender) {
		return outOfGas()
	}
	if p.balSender.Lt(&p.amount) {
		countNativeAsset(&NativeAssetCounters.FallbackBalance)
		return false
	}
	var newSender uint256.Int
	newSender.Sub(&p.balSender, &p.amount)
	if !sstore(6, p.keySender, &p.balSender, &newSender) {
		return outOfGas()
	}
	// For a self transfer this key is already loaded, so the read is a cache
	// hit, and the value that matters is the one the sender's write left.
	if !load(7, p.keyTo, &p.balTo) {
		return outOfGas()
	}
	curTo := p.balTo
	if p.keyTo == p.keySender {
		curTo = newSender
	}
	var newTo uint256.Int
	if _, overflow := newTo.AddOverflow(&curTo, &p.amount); overflow {
		countNativeAsset(&NativeAssetCounters.FallbackBalance)
		return false
	}
	if !sstore(8, p.keyTo, &curTo, &newTo) || !charge(p.segments[9]) {
		return outOfGas()
	}
	p.gas = c.Gas - left
	return true
}

// applyUSDTTransfer performs a planned transfer in bytecode order: access list,
// refunds, storage, gas and the Transfer event.
func (evm *EVM) applyUSDTTransfer(c *Contract, p *usdtPlan) {
	ibs := evm.intraBlockState
	warm := usdtWarm{ibs: ibs}
	recorder := evm.config.SlotRecorder
	sload := func(slot types.Hash) uint64 {
		if recorder != nil {
			recorder.RecordSlotAccess(usdtAddress, slot)
		}
		return warm.sload(slot, true)
	}

	gas := p.segments[0] + sload(usdtSlot(usdtSlotOwnerPaused))
	gas += p.segments[1] + sload(p.keyBlacklist)
	gas += p.segments[2] + sload(usdtSlot(usdtSlotUpgraded))
	gas += p.segments[3] + sload(usdtSlot(usdtSlotFeeRate))
	gas += p.segments[4] + sload(usdtSlot(usdtSlotMaxFee))
	gas += p.segments[5] + sload(p.keySender)

	var newSender uint256.Int
	newSender.Sub(&p.balSender, &p.amount)
	gas += p.segments[6] + usdtSStore(ibs, p.keySender, &p.balSender, &newSender, true)
	ibs.SetState(usdtAddress, &p.keySender, newSender)

	curTo := p.balTo
	if p.keyTo == p.keySender {
		curTo = newSender
	}
	gas += p.segments[7] + sload(p.keyTo)
	var newTo uint256.Int
	newTo.Add(&curTo, &p.amount)
	gas += p.segments[8] + usdtSStore(ibs, p.keyTo, &curTo, &newTo, true)
	ibs.SetState(usdtAddress, &p.keyTo, newTo)

	c.Gas -= gas + p.segments[9]

	blockNum := evm.context.BlockNumber
	var l *block.Log
	if alloc, ok := ibs.(logAllocator); ok {
		l = alloc.NewLog(usdtAddress, 3, 32, blockNum)
	} else {
		l = &block.Log{
			Address:     usdtAddress,
			Topics:      make([]types.Hash, 3),
			Data:        make([]byte, 32),
			BlockNumber: uint256.NewInt(blockNum),
		}
	}
	var from, to types.Hash
	copy(from[12:], p.sender[:])
	copy(to[12:], p.to[:])
	l.Topics[0] = erc20TransferTopic
	l.Topics[1] = from
	l.Topics[2] = to
	p.amount.WriteToSlice(l.Data)
	ibs.AddLog(l)
}

// shadowUSDTTransfer runs the module, records its effects, rolls them back,
// runs the interpreter, records again and compares. The interpreter's result
// is what the caller receives.
func (evm *EVM) shadowUSDTTransfer(c *Contract, input []byte, p *usdtPlan) ([]byte, error) {
	base := evm.intraBlockState
	startGas := c.Gas

	snap := base.Snapshot()
	native := &nativeAssetRecorder{IntraBlockState: base}
	evm.intraBlockState = native
	evm.applyUSDTTransfer(c, p)
	nativeLeft := c.Gas
	evm.intraBlockState = base
	base.RevertToSnapshot(snap)
	c.Gas = startGas

	interp := &nativeAssetRecorder{IntraBlockState: base}
	evm.intraBlockState = interp
	ret, err := run(evm, c, input, false)
	evm.intraBlockState = base

	diff := native.diff(interp)
	if diff == "" && (err != nil || len(ret) != 0) {
		diff = fmt.Sprintf("interpreter returned ret=%x err=%v for an accepted call", ret, err)
	}
	if diff == "" && (nativeLeft != c.Gas || startGas-nativeLeft != p.gas) {
		diff = fmt.Sprintf("gas used native %d (planned %d) interpreter %d", startGas-nativeLeft, p.gas, startGas-c.Gas)
	}
	if diff == "" {
		countNativeAsset(&NativeAssetCounters.ShadowMatched)
	} else if n := NativeAssetCounters.ShadowMismatch.Add(1); n <= 20 {
		log.Warn("Native asset shadow mismatch", "block", evm.context.BlockNumber,
			"tx", evm.txContext.TxHash, "sender", p.sender, "to", p.to, "amount", p.amount.String(), "diff", diff)
	}
	return ret, err
}

type recordedWrite struct {
	addr  types.Address
	slot  types.Hash
	value uint256.Int
}

type recordedLog struct {
	addr   types.Address
	topics []types.Hash
	data   []byte
}

type recordedSlot struct {
	addr types.Address
	slot types.Hash
}

// nativeAssetRecorder wraps a state and records the effects the comparison
// needs, delegating every call.
type nativeAssetRecorder struct {
	evmtypes.IntraBlockState
	writes  []recordedWrite
	logs    []recordedLog
	refunds []int64
	slots   []recordedSlot
}

func (r *nativeAssetRecorder) SetState(addr types.Address, key *types.Hash, value uint256.Int) {
	r.writes = append(r.writes, recordedWrite{addr: addr, slot: *key, value: value})
	r.IntraBlockState.SetState(addr, key, value)
}

func (r *nativeAssetRecorder) AddLog(l *block.Log) {
	r.logs = append(r.logs, recordedLog{
		addr:   l.Address,
		topics: append([]types.Hash(nil), l.Topics...),
		data:   append([]byte(nil), l.Data...),
	})
	r.IntraBlockState.AddLog(l)
}

func (r *nativeAssetRecorder) AddRefund(gas uint64) {
	r.refunds = append(r.refunds, int64(gas))
	r.IntraBlockState.AddRefund(gas)
}

func (r *nativeAssetRecorder) SubRefund(gas uint64) {
	r.refunds = append(r.refunds, -int64(gas))
	r.IntraBlockState.SubRefund(gas)
}

func (r *nativeAssetRecorder) AddSlotToAccessList(addr types.Address, slot types.Hash) {
	r.slots = append(r.slots, recordedSlot{addr: addr, slot: slot})
	r.IntraBlockState.AddSlotToAccessList(addr, slot)
}

// diff returns "" when r and o recorded the same effects. Writes, logs and
// refund adjustments must match in order; access-list additions as a set.
func (r *nativeAssetRecorder) diff(o *nativeAssetRecorder) string {
	if len(r.writes) != len(o.writes) {
		return fmt.Sprintf("writes: native %d interpreter %d", len(r.writes), len(o.writes))
	}
	for i := range r.writes {
		if r.writes[i] != o.writes[i] {
			return fmt.Sprintf("write %d: native %x=%s interpreter %x=%s", i,
				r.writes[i].slot, r.writes[i].value.String(), o.writes[i].slot, o.writes[i].value.String())
		}
	}
	if len(r.refunds) != len(o.refunds) {
		return fmt.Sprintf("refunds: native %v interpreter %v", r.refunds, o.refunds)
	}
	for i := range r.refunds {
		if r.refunds[i] != o.refunds[i] {
			return fmt.Sprintf("refunds: native %v interpreter %v", r.refunds, o.refunds)
		}
	}
	if len(r.logs) != len(o.logs) {
		return fmt.Sprintf("logs: native %d interpreter %d", len(r.logs), len(o.logs))
	}
	for i := range r.logs {
		a, b := r.logs[i], o.logs[i]
		if a.addr != b.addr || len(a.topics) != len(b.topics) || !bytes.Equal(a.data, b.data) {
			return fmt.Sprintf("log %d differs", i)
		}
		for j := range a.topics {
			if a.topics[j] != b.topics[j] {
				return fmt.Sprintf("log %d topic %d differs", i, j)
			}
		}
	}
	if len(r.slots) != len(o.slots) {
		return fmt.Sprintf("access list additions: native %d interpreter %d", len(r.slots), len(o.slots))
	}
	for _, s := range r.slots {
		found := false
		for _, t := range o.slots {
			if s == t {
				found = true
				break
			}
		}
		if !found {
			return fmt.Sprintf("access list: native warmed %x the interpreter did not", s.slot)
		}
	}
	return ""
}
