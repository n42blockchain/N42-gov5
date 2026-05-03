// trace_tx.go — minimal step-trace tracer for ethexec, gated by env var
// N42_TRACE_TX=<txhash>. Emits one JSON object per opcode step,
// compatible enough with reth's debug_traceTransaction structLog format
// to support side-by-side diff.
package ethel

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	vm2 "github.com/n42blockchain/N42/internal/vm"
)

// NoopTracer satisfies EVMLogger but does nothing. Used to test whether
// merely attaching a tracer (and setting cfg.Debug=true) changes EVM
// behavior versus the no-tracer fast path.
//
// Variant controlled by N42_NOOP_TRACER_LEVEL env var (read at process
// init). 0=pure empty, 1=read stack, 2=fmt.Sprintf no I/O, 3=syscall.
// Used to bisect what kind of "work" by tracer (memory access vs heap
// alloc vs syscall barrier) fixes the txType=1 / EIP-2930 anomaly.
type NoopTracer struct{}

var noopLevel = func() int {
	switch os.Getenv("N42_NOOP_TRACER_LEVEL") {
	case "1":
		return 1
	case "2":
		return 2
	case "3":
		return 3
	default:
		return 0
	}
}()
var noopSink uint64 // accumulator for stack reads (prevents compiler dead-elim)

func (NoopTracer) CaptureTxStart(uint64) {}
func (NoopTracer) CaptureTxEnd(uint64)   {}
func (NoopTracer) CaptureStart(env vm2.VMInterface, from types.Address, to types.Address, create bool, input []byte, gas uint64, value *uint256.Int) {
}
func (NoopTracer) CaptureEnd([]byte, uint64, error) {}
func (NoopTracer) CaptureEnter(typ vm2.OpCode, from types.Address, to types.Address, input []byte, gas uint64, value *uint256.Int) {
}
func (NoopTracer) CaptureExit([]byte, uint64, error) {}
func (NoopTracer) CaptureState(pc uint64, op vm2.OpCode, gas, cost uint64, scope *vm2.ScopeContext, rData []byte, depth int, err error) {
	if noopLevel == 0 {
		return
	}
	if noopLevel >= 1 {
		// Read stack (mimic StepTracer's stack access — forces memory loads)
		if scope != nil && scope.Stack != nil {
			data := scope.Stack.Data
			for i := range data {
				noopSink += data[i].Uint64()
			}
		}
		// Also read contract.Gas + scope.Contract fields
		if scope != nil && scope.Contract != nil {
			noopSink += scope.Contract.Gas
		}
	}
	if noopLevel >= 2 {
		// Heap alloc + format work (Sprintf allocates)
		_ = fmt.Sprintf("pc=%d op=%s gas=%d cost=%d", pc, op.String(), gas, cost)
	}
	if noopLevel >= 3 {
		// Syscall barrier — write to discard sink (forces kernel call)
		fmt.Fprintf(io.Discard, "pc=%d op=%s gas=%d", pc, op.String(), gas)
	}
}
func (NoopTracer) CaptureFault(pc uint64, op vm2.OpCode, gas, cost uint64, scope *vm2.ScopeContext, depth int, err error) {
}

type StepTracer struct {
	w     io.Writer
	first bool
}

func NewStepTracer(w io.Writer) *StepTracer {
	fmt.Fprint(w, `{"structLogs":[`)
	return &StepTracer{w: w, first: true}
}

func (t *StepTracer) Close(failed bool, gas uint64, ret []byte) {
	fmt.Fprintf(t.w, `],"failed":%v,"gas":%d,"returnValue":"%x"}`, failed, gas, ret)
}

func (t *StepTracer) CaptureTxStart(gasLimit uint64) {}
func (t *StepTracer) CaptureTxEnd(restGas uint64)    {}
func (t *StepTracer) CaptureStart(env vm2.VMInterface, from types.Address, to types.Address, create bool, input []byte, gas uint64, value *uint256.Int) {
	fmt.Fprintf(t.w, "/*captureStart: from=%x to=%x create=%v input=%dB gas=%d*/", from, to, create, len(input), gas)
}
func (t *StepTracer) CaptureEnd(output []byte, usedGas uint64, err error) {}
func (t *StepTracer) CaptureEnter(typ vm2.OpCode, from types.Address, to types.Address, input []byte, gas uint64, value *uint256.Int) {
}
func (t *StepTracer) CaptureExit(output []byte, usedGas uint64, err error) {}

func (t *StepTracer) CaptureState(pc uint64, op vm2.OpCode, gas, cost uint64, scope *vm2.ScopeContext, rData []byte, depth int, err error) {
	if !t.first {
		fmt.Fprint(t.w, ",")
	}
	t.first = false

	// stack: top is last
	stk := scope.Stack
	stkOut := "["
	if stk != nil {
		data := stk.Data
		for i, v := range data {
			if i > 0 {
				stkOut += ","
			}
			b := v.Bytes()
			if len(b) == 0 {
				stkOut += `"0x0"`
			} else {
				stkOut += `"0x` + hex.EncodeToString(b) + `"`
			}
		}
	}
	stkOut += "]"

	errStr := ""
	if err != nil {
		errStr = fmt.Sprintf(`,"error":%q`, err.Error())
	}
	fmt.Fprintf(t.w, `{"pc":%d,"op":"%s","gas":%d,"gasCost":%d,"depth":%d,"stack":%s%s}`,
		pc, op.String(), gas, cost, depth, stkOut, errStr)
}

func (t *StepTracer) CaptureFault(pc uint64, op vm2.OpCode, gas, cost uint64, scope *vm2.ScopeContext, depth int, err error) {
	t.CaptureState(pc, op, gas, cost, scope, nil, depth, err)
}

// Avoid unused imports.
var _ transaction.Transaction
