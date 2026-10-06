package runtime

// Pins the native asset module to the real USDT bytecode: every scenario runs
// once through the interpreter and once with NativeAssetTime set, from the same
// committed state, and must end with the same error, remaining gas, refund
// counter, storage, logs, access list and state-read order. Scenarios the
// module does not model must be handed back to the interpreter.

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

var (
	naUSDT   = types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7")
	naSender = types.HexToAddress("0x1111111111111111111111111111111111111111")
	naRecip  = types.HexToAddress("0x2222222222222222222222222222222222222222")
	naOwner  = types.HexToAddress("0x3333333333333333333333333333333333333333")
	naRouter = types.HexToAddress("0x4444444444444444444444444444444444444444")
	naEntry  = types.HexToAddress("0x5555555555555555555555555555555555555555")
)

func naCode(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../testdata/usdt_runtime.hex")
	if err != nil {
		t.Fatal(err)
	}
	code, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	want := types.HexToHash("0xb44fb4e949d0f78f87f79ee46428f23a2a5713ce6fc6e0beb3dda78c2ac1ea55")
	if got := crypto.Keccak256Hash(code); got != want {
		t.Fatalf("testdata USDT code hash %x, want %x", got, want)
	}
	return code
}

func naSlot(n uint64) types.Hash {
	var h types.Hash
	h[31] = byte(n)
	return h
}

func naKey(a types.Address, slot uint64) types.Hash {
	var buf [64]byte
	copy(buf[12:32], a[:])
	buf[63] = byte(slot)
	return crypto.Keccak256Hash(buf[:])
}

func naBal(a types.Address) types.Hash { return naKey(a, 2) }

// naForward copies 68 bytes of calldata and CALLs (0xf1) or STATICCALLs (0xfa)
// target with all remaining gas.
func naForward(target types.Address, static bool) []byte {
	code := []byte{0x60, 0x44, 0x60, 0x00, 0x60, 0x00, 0x37, 0x60, 0x00, 0x60, 0x00, 0x60, 0x44, 0x60, 0x00}
	if !static {
		code = append(code, 0x60, 0x00) // value
	}
	code = append(code, 0x73)
	code = append(code, target[:]...)
	op := byte(0xf1)
	if static {
		op = 0xfa
	}
	return append(code, 0x5a, op, 0x50, 0x00)
}

func naChainConfig(enabled bool) *params.ChainConfig {
	c := &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        new(big.Int),
		TangerineWhistleBlock: new(big.Int),
		SpuriousDragonBlock:   new(big.Int),
		ByzantiumBlock:        new(big.Int),
		ConstantinopleBlock:   new(big.Int),
		PetersburgBlock:       new(big.Int),
		IstanbulBlock:         new(big.Int),
		MuirGlacierBlock:      new(big.Int),
		BerlinBlock:           new(big.Int),
		LondonBlock:           new(big.Int),
		ArrowGlacierBlock:     new(big.Int),
		GrayGlacierBlock:      new(big.Int),
		ShanghaiBlock:         new(big.Int),
		CancunBlock:           new(big.Int),
		PragueTime:            new(big.Int),
	}
	if enabled {
		c.NativeAssetTime = new(big.Int)
	}
	return c
}

type naCase struct {
	name    string
	storage map[types.Hash]*uint256.Int // committed USDT storage, on top of the defaults
	dirty   map[types.Hash]*uint256.Int // written inside the tx before the call
	refund  uint64                      // refund counter before the call
	prewarm []types.Hash
	via     string // "" direct, "router" (CALL at depth 1), "static" (CALL inside STATICCALL)
	to      types.Address
	amount  *uint256.Int
	input   []byte // overrides the transfer calldata
	value   uint64
	gas     uint64
	native  bool // the module must take the call
}

type naOutcome struct {
	err     string
	gasLeft uint64
	refund  uint64
	storage string
	logs    string
	warm    string
	reads   string
	native  uint64
}

// naReadLog records the reads a state makes against its underlying reader. A
// witness stream is positional -- values only, in execution order -- so the
// module must reproduce this sequence exactly, hand-backs included.
type naReadLog struct {
	state.StateReader
	seq []string
}

func (r *naReadLog) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	r.seq = append(r.seq, fmt.Sprintf("acct:%x", a[:4]))
	return r.StateReader.ReadAccountData(a)
}

func (r *naReadLog) ReadAccountStorage(a types.Address, key *types.Hash) ([]byte, error) {
	r.seq = append(r.seq, fmt.Sprintf("slot:%x/%x", a[:4], key[:4]))
	return r.StateReader.ReadAccountStorage(a, key)
}

const (
	naSenderBal = 1_000_000_000
	naRecipBal  = 5_000_000
	naAmount    = 1_000_000
)

func naTransferInput(to types.Address, amount *uint256.Int) []byte {
	in := make([]byte, 68)
	copy(in, []byte{0xa9, 0x05, 0x9c, 0xbb})
	copy(in[16:36], to[:])
	amount.WriteToSlice(in[36:68])
	return in
}

// naBase commits the contracts and the case's storage into a fresh memdb. A
// call never commits, so one base serves any number of executions.
func naBase(t *testing.T, code []byte, tc naCase) kv.RwTx {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	holder := naSender
	if tc.via != "" {
		holder = naRouter
	}
	ibs := state.New(state.NewPlainStateReader(tx))
	ibs.CreateAccount(naSender, false)
	ibs.SetBalance(naSender, uint256.NewInt(1e18))
	ibs.CreateAccount(naUSDT, true)
	ibs.SetCode(naUSDT, code)
	ibs.CreateAccount(naRouter, true)
	ibs.SetCode(naRouter, naForward(naUSDT, false))
	ibs.CreateAccount(naEntry, true)
	ibs.SetCode(naEntry, naForward(naRouter, true))
	base := map[types.Hash]*uint256.Int{
		naSlot(0):      new(uint256.Int).SetBytes(naOwner[:]),
		naBal(holder):  uint256.NewInt(naSenderBal),
		naBal(naRecip): uint256.NewInt(naRecipBal),
	}
	for k, v := range tc.storage {
		base[k] = v
	}
	for k, v := range base {
		k := k
		ibs.SetState(naUSDT, &k, *v)
	}
	if err := ibs.CommitBlock(naChainConfig(false).Rules(1), state.NewPlainStateWriter(tx, tx, 1)); err != nil {
		t.Fatal(err)
	}
	return tx
}

func naInput(tc naCase) []byte {
	if tc.input != nil {
		return tc.input
	}
	to, amount := tc.to, tc.amount
	if to == (types.Address{}) {
		to = naRecip
	}
	if amount == nil {
		amount = uint256.NewInt(naAmount)
	}
	return naTransferInput(to, amount)
}

// naExec runs the case's call with gas against a fresh state over base.
func naExec(t *testing.T, base kv.RwTx, tc naCase, enabled bool, gas uint64, tracer vm.EVMLogger) naOutcome {
	t.Helper()
	cc := naChainConfig(enabled)
	rules := cc.Rules(1)
	reads := &naReadLog{StateReader: state.NewPlainStateReader(base)}
	ibs := state.New(reads)
	cfg := &Config{
		ChainConfig: cc, Origin: naSender, BlockNumber: big.NewInt(1), Time: big.NewInt(1),
		GasLimit: 1_000_000, GasPrice: new(uint256.Int), Value: uint256.NewInt(tc.value), State: ibs,
	}
	if tracer != nil {
		cfg.EVMConfig = vm.Config{Debug: true, Tracer: tracer}
	}
	setDefaults(cfg)
	env := NewEnv(cfg)
	target := naUSDT
	switch tc.via {
	case "router":
		target = naRouter
	case "static":
		target = naEntry
	}
	ibs.PrepareAccessList(naSender, &target, vm.ActivePrecompiles(rules), nil)
	for k, v := range tc.dirty {
		k := k
		ibs.SetState(naUSDT, &k, *v)
	}
	if tc.refund > 0 {
		ibs.AddRefund(tc.refund)
	}
	for _, s := range tc.prewarm {
		ibs.AddSlotToAccessList(naUSDT, s)
	}

	before := vm.NativeAssetCounters.Native.Load()
	mark := len(reads.seq)
	_, left, err := env.Call(vm.AccountRef(naSender), target, naInput(tc), gas, cfg.Value, false)
	callReads := strings.Join(reads.seq[mark:], " ")

	out := naOutcome{gasLeft: left, refund: ibs.GetRefund(), reads: callReads, native: vm.NativeAssetCounters.Native.Load() - before}
	if err != nil {
		out.err = err.Error()
	}
	keys := []types.Hash{
		naSlot(0), naSlot(3), naSlot(4), naSlot(10),
		naBal(naSender), naBal(naRouter), naBal(naRecip), naBal(naOwner),
		naKey(naSender, 6), naKey(naRouter, 6),
	}
	var sb, wb strings.Builder
	for _, k := range keys {
		k := k
		var v uint256.Int
		ibs.GetState(naUSDT, &k, &v)
		_, present := ibs.SlotInAccessList(naUSDT, k)
		fmt.Fprintf(&sb, "%x=%s ", k[:4], v.String())
		fmt.Fprintf(&wb, "%x=%v ", k[:4], present)
	}
	out.storage, out.warm = sb.String(), wb.String()
	var lb strings.Builder
	for _, l := range ibs.Logs() {
		fmt.Fprintf(&lb, "%x|%x|%x;", l.Address, l.Topics, l.Data)
	}
	out.logs = lb.String()
	return out
}

func naRun(t *testing.T, code []byte, tc naCase, enabled bool) naOutcome {
	t.Helper()
	return naExec(t, naBase(t, code, tc), tc, enabled, tc.gas, nil)
}

func naCases() []naCase {
	big := func(v uint64) *uint256.Int { return uint256.NewInt(v) }
	allSlots := []types.Hash{naSlot(0), naKey(naSender, 6), naSlot(10), naSlot(3), naSlot(4), naBal(naSender), naBal(naRecip)}
	flag160 := new(uint256.Int).Lsh(uint256.NewInt(1), 160)
	paused := new(uint256.Int).Or(flag160, new(uint256.Int).SetBytes(naOwner[:]))
	maxRecip := new(uint256.Int).Sub(new(uint256.Int).SetAllOne(), uint256.NewInt(naAmount-1))
	dirtyAddr := naTransferInput(naRecip, big(naAmount))
	dirtyAddr[4] = 1

	return []naCase{
		{name: "recipient already holds", gas: 200_000, native: true},
		{name: "recipient empty", storage: map[types.Hash]*uint256.Int{naBal(naRecip): big(0)}, gas: 200_000, native: true},
		{name: "sender sends everything", amount: big(naSenderBal), gas: 200_000, native: true},
		{name: "self transfer", to: naSender, gas: 200_000, native: true},
		{name: "zero amount", amount: big(0), gas: 200_000, native: true},
		{name: "zero amount to empty recipient", amount: big(0), storage: map[types.Hash]*uint256.Int{naBal(naRecip): big(0)}, gas: 200_000, native: true},
		{name: "slots prewarmed", prewarm: allSlots, gas: 200_000, native: true},
		{name: "sender dirty, recipient cleared earlier", dirty: map[types.Hash]*uint256.Int{naBal(naSender): big(naSenderBal + 5), naBal(naRecip): big(0)}, refund: 4800, gas: 200_000, native: true},
		{name: "sender back to original", dirty: map[types.Hash]*uint256.Int{naBal(naSender): big(naSenderBal + naAmount)}, gas: 200_000, native: true},
		{name: "recipient back to zero original", storage: map[types.Hash]*uint256.Int{naBal(naRecip): big(0)}, dirty: map[types.Hash]*uint256.Int{naBal(naRecip): big(7)}, to: naRecip, gas: 200_000, native: true},
		{name: "via router", via: "router", gas: 300_000, native: true},
		{name: "inside static call", via: "static", gas: 300_000, native: false},
		{name: "paused", storage: map[types.Hash]*uint256.Int{naSlot(0): paused}, gas: 200_000, native: false},
		{name: "sender blacklisted", storage: map[types.Hash]*uint256.Int{naKey(naSender, 6): big(1)}, gas: 200_000, native: false},
		{name: "deprecated", storage: map[types.Hash]*uint256.Int{naSlot(10): flag160}, gas: 200_000, native: false},
		{name: "fee enabled", storage: map[types.Hash]*uint256.Int{naSlot(3): big(10), naSlot(4): big(1_000_000)}, gas: 200_000, native: false},
		{name: "insufficient balance", amount: big(naSenderBal + 1), gas: 200_000, native: false},
		{name: "recipient overflow", storage: map[types.Hash]*uint256.Int{naBal(naRecip): maxRecip}, gas: 200_000, native: false},
		{name: "calldata 69 bytes", input: append(naTransferInput(naRecip, big(naAmount)), 0), gas: 200_000, native: false},
		{name: "dirty address word", input: dirtyAddr, gas: 200_000, native: false},
		{name: "value attached", value: 1, gas: 200_000, native: false},
		// The module takes a call exactly when the interpreter would finish it.
		// This path needs 24,501 gas, and the SSTORE sentry already holds by
		// then, so the boundary is sharp.
		{name: "exact gas", gas: 24_501, native: true},
		{name: "one gas short", gas: 24_500, native: false},
		{name: "gas runs out early", gas: 20_000, native: false},
	}
}

func naCompare(t *testing.T, label string, got, ref naOutcome) {
	t.Helper()
	if got.err != ref.err {
		t.Errorf("%s err: native %q interpreter %q", label, got.err, ref.err)
	}
	if got.gasLeft != ref.gasLeft {
		t.Errorf("%s gas left: native %d interpreter %d", label, got.gasLeft, ref.gasLeft)
	}
	if got.refund != ref.refund {
		t.Errorf("%s refund: native %d interpreter %d", label, got.refund, ref.refund)
	}
	if got.storage != ref.storage {
		t.Errorf("%s storage:\n native      %s\n interpreter %s", label, got.storage, ref.storage)
	}
	if got.logs != ref.logs {
		t.Errorf("%s logs:\n native      %s\n interpreter %s", label, got.logs, ref.logs)
	}
	if got.warm != ref.warm {
		t.Errorf("%s access list:\n native      %s\n interpreter %s", label, got.warm, ref.warm)
	}
	if got.reads != ref.reads {
		t.Errorf("%s state reads (witness order):\n native      %s\n interpreter %s", label, got.reads, ref.reads)
	}
}

func TestNativeAssetMatchesBytecode(t *testing.T) {
	code := naCode(t)
	vm.NativeAssetStatsEnabled = true
	t.Cleanup(func() { vm.NativeAssetStatsEnabled = false })

	for _, tc := range naCases() {
		t.Run(tc.name, func(t *testing.T) {
			ref := naRun(t, code, tc, false)
			got := naRun(t, code, tc, true)
			if ref.native != 0 {
				t.Fatalf("module ran with NativeAssetTime unset")
			}
			if tc.native != (got.native == 1) {
				t.Fatalf("module took the call = %v, want %v (interpreter err %q)", got.native == 1, tc.native, ref.err)
			}
			naCompare(t, "", got, ref)
		})
	}
}

// TestNativeAssetGasSweep runs every modeled case at every gas value up to what
// the call needs. Each value either lets the module finish or stops the
// interpreter somewhere on the path, and both paths must agree on all of it --
// in particular on which slots were read before the gas ran out.
func TestNativeAssetGasSweep(t *testing.T) {
	if testing.Short() {
		t.Skip("sweeps every gas value up to each transfer's cost")
	}
	code := naCode(t)
	vm.NativeAssetStatsEnabled = true
	t.Cleanup(func() { vm.NativeAssetStatsEnabled = false })
	everyValue := map[string]bool{
		"recipient already holds": true, "recipient empty": true, "zero amount": true,
		"self transfer": true, "via router": true,
	}
	for _, tc := range naCases() {
		if !tc.native {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			base := naBase(t, code, tc)
			full := naExec(t, base, tc, false, tc.gas, nil)
			top := tc.gas - full.gasLeft + params.SstoreSentryGasEIP2200 + 200
			step := uint64(29)
			if everyValue[tc.name] {
				step = 1
			}
			took, values := 0, 0
			for g := uint64(0); g <= top; g += step {
				ref := naExec(t, base, tc, false, g, nil)
				got := naExec(t, base, tc, true, g, nil)
				values++
				if got.native == 1 {
					took++
				}
				before := t.Failed()
				naCompare(t, fmt.Sprintf("gas %d:", g), got, ref)
				if !before && t.Failed() {
					t.FailNow()
				}
			}
			if took == 0 {
				t.Fatalf("the module never took the call in %d gas values up to %d", values, top)
			}
			t.Logf("%d gas values up to %d, module took %d", values, top, took)
		})
	}
}

// TestNativeAssetShadowAgrees runs the matrix in shadow mode: the comparison
// the full-year replay relies on must itself report no mismatch on known-good
// scenarios, and must keep the interpreter's outcome.
func TestNativeAssetShadowAgrees(t *testing.T) {
	code := naCode(t)
	vm.NativeAssetStatsEnabled = true
	vm.NativeAssetShadow = true
	t.Cleanup(func() {
		vm.NativeAssetStatsEnabled = false
		vm.NativeAssetShadow = false
	})
	mismatch0 := vm.NativeAssetCounters.ShadowMismatch.Load()
	matched0 := vm.NativeAssetCounters.ShadowMatched.Load()
	want := uint64(0)
	for _, tc := range naCases() {
		ref := naRun(t, code, tc, false)
		got := naRun(t, code, tc, true)
		if got != ref {
			t.Errorf("%s: shadow outcome differs from the interpreter:\n shadow      %+v\n interpreter %+v", tc.name, got, ref)
		}
		if tc.native {
			want++
		}
	}
	if n := vm.NativeAssetCounters.ShadowMismatch.Load() - mismatch0; n != 0 {
		t.Fatalf("%d shadow mismatches", n)
	}
	if n := vm.NativeAssetCounters.ShadowMatched.Load() - matched0; n != want {
		t.Fatalf("shadow matched %d calls, want %d", n, want)
	}
}

type naStep struct {
	op        vm.OpCode
	gas, cost uint64
}

// naTracer records the gas at every opcode of a call.
type naTracer struct{ steps []naStep }

func (n *naTracer) CaptureTxStart(uint64) {}
func (n *naTracer) CaptureTxEnd(uint64)   {}
func (n *naTracer) CaptureStart(vm.VMInterface, types.Address, types.Address, bool, []byte, uint64, *uint256.Int) {
}
func (n *naTracer) CaptureEnd([]byte, uint64, error) {}
func (n *naTracer) CaptureEnter(vm.OpCode, types.Address, types.Address, []byte, uint64, *uint256.Int) {
}
func (n *naTracer) CaptureExit([]byte, uint64, error) {}
func (n *naTracer) CaptureState(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, rData []byte, depth int, err error) {
	n.steps = append(n.steps, naStep{op: op, gas: gas, cost: cost})
}
func (n *naTracer) CaptureFault(uint64, vm.OpCode, uint64, uint64, *vm.ScopeContext, int, error) {}

// TestNativeAssetGasSegments measures, opcode by opcode, the static gas the
// bytecode spends between its storage accesses and pins the module's table to
// it. The module charges these segments to decide how far the interpreter would
// get on a given amount of gas.
func TestNativeAssetGasSegments(t *testing.T) {
	code := naCode(t)
	for _, zero := range []bool{false, true} {
		tc := naCase{name: "segments", gas: 200_000}
		if zero {
			tc.amount = uint256.NewInt(0)
		}
		tr := &naTracer{}
		out := naExec(t, naBase(t, code, tc), tc, false, tc.gas, tr)
		if out.err != "" {
			t.Fatalf("zero=%v: %s", zero, out.err)
		}
		var segs [10]uint64
		var ops []string
		prevEnd := tc.gas
		for _, s := range tr.steps {
			if s.op != vm.SLOAD && s.op != vm.SSTORE {
				continue
			}
			if len(ops) == 9 {
				t.Fatalf("zero=%v: more than nine storage accesses", zero)
			}
			segs[len(ops)] = prevEnd - s.gas
			ops = append(ops, s.op.String())
			prevEnd = s.gas - s.cost
		}
		segs[9] = prevEnd - out.gasLeft
		if got, want := strings.Join(ops, " "), "SLOAD SLOAD SLOAD SLOAD SLOAD SLOAD SSTORE SLOAD SSTORE"; got != want {
			t.Fatalf("zero=%v: storage accesses %q, want %q", zero, got, want)
		}
		if want := vm.USDTTransferGasSegments(zero); segs != want {
			t.Fatalf("zero=%v: bytecode segments %v, module table %v", zero, segs, want)
		}
	}
}

func TestNativeAssetTransferTopic(t *testing.T) {
	got := crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))
	want := types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	if got != want {
		t.Fatalf("Transfer topic %x, want %x", got, want)
	}
}
