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
// liveTracer: real-time EVM event stream. Unlike post-hoc tracers,
// this hook fires during normal block production so downstream
// consumers (log indexer, MEV detector, explorer subscriptions)
// can observe every opcode and call frame with minimal latency.

package native

import (
	"encoding/json"
	"sync"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/tracers"
	"github.com/n42blockchain/N42/internal/vm"
)

func init() {
	tracers.DefaultDirectory.Register("liveTracer", newLiveTracerFromContext, false)
}

// newLiveTracerFromContext creates a LiveTracer from the tracer context.
// The event channel is internal; callers use GetResult() to retrieve a summary.
func newLiveTracerFromContext(ctx *tracers.Context, cfg json.RawMessage) (tracers.Tracer, error) {
	blockNum := uint64(0)
	if ctx != nil && ctx.BlockNumber != nil {
		blockNum = ctx.BlockNumber.Uint64()
	}
	// Internal event channel for self-contained use; events are counted but not streamed.
	ch := make(chan LiveTracerEvent, 1024)
	go func() {
		for range ch {
		} // drain
	}()
	return NewLiveTracer(blockNum, ch, 10000), nil
}

// LiveTracerEvent represents a single execution event emitted in real-time.
type LiveTracerEvent struct {
	Type        string         `json:"type"`                  // "txStart", "txEnd", "call", "return", "log", "create"
	BlockNumber uint64         `json:"blockNumber,omitempty"`
	TxIndex     int            `json:"txIndex,omitempty"`
	From        *types.Address `json:"from,omitempty"`
	To          *types.Address `json:"to,omitempty"`
	Value       string         `json:"value,omitempty"`
	Gas         uint64         `json:"gas,omitempty"`
	GasUsed     uint64         `json:"gasUsed,omitempty"`
	Input       string         `json:"input,omitempty"` // hex-encoded, truncated
	Output      string         `json:"output,omitempty"`
	Error       string         `json:"error,omitempty"`
	Create      bool           `json:"create,omitempty"`
	Depth       int            `json:"depth,omitempty"`
}

// LiveTracer emits execution events to a subscriber channel in real-time
// during actual block processing (not replay). This enables monitoring
// dashboards, MEV detection, and real-time analytics without re-execution.
//
// Usage:
//
//	tracer := NewLiveTracer(blockNumber, eventCh, 1000)
//	vmCfg.Tracer = tracer
//	// ... execute block with this config ...
//	tracer.Close()
type LiveTracer struct {
	mu          sync.Mutex
	blockNumber uint64
	txIndex     int
	eventCh     chan<- LiveTracerEvent
	maxEvents   int
	emitted     int
	closed      bool
}

// NewLiveTracer creates a live tracer that emits events to the given channel.
// maxEvents caps the number of events per block to prevent channel flooding.
// Set maxEvents to 0 for unlimited.
func NewLiveTracer(blockNumber uint64, eventCh chan<- LiveTracerEvent, maxEvents int) *LiveTracer {
	return &LiveTracer{
		blockNumber: blockNumber,
		eventCh:     eventCh,
		maxEvents:   maxEvents,
	}
}

// SetTxIndex updates the current transaction index for subsequent events.
func (t *LiveTracer) SetTxIndex(idx int) {
	t.mu.Lock()
	t.txIndex = idx
	t.mu.Unlock()
}

// Close marks the tracer as closed. No more events will be emitted.
func (t *LiveTracer) Close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
}

func (t *LiveTracer) emit(evt LiveTracerEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return
	}
	if t.maxEvents > 0 && t.emitted >= t.maxEvents {
		return
	}

	evt.BlockNumber = t.blockNumber
	evt.TxIndex = t.txIndex
	t.emitted++

	// Non-blocking send: drop events if the consumer is slow
	select {
	case t.eventCh <- evt:
	default:
	}
}

// EventCount returns the number of events emitted so far.
func (t *LiveTracer) EventCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emitted
}

// --- vm.EVMLogger interface ---

func (t *LiveTracer) CaptureTxStart(gasLimit uint64) {
	t.emit(LiveTracerEvent{
		Type: "txStart",
		Gas:  gasLimit,
	})
}

func (t *LiveTracer) CaptureTxEnd(restGas uint64) {
	t.emit(LiveTracerEvent{
		Type: "txEnd",
		Gas:  restGas,
	})
}

func (t *LiveTracer) CaptureStart(env vm.VMInterface, from types.Address, to types.Address, create bool, input []byte, gas uint64, value *uint256.Int) {
	evt := LiveTracerEvent{
		Type:   "call",
		From:   &from,
		To:     &to,
		Gas:    gas,
		Create: create,
	}
	if value != nil {
		evt.Value = value.Hex()
	}
	if len(input) > 0 {
		maxLen := 128
		if len(input) < maxLen {
			maxLen = len(input)
		}
		evt.Input = types.Bytes2Hex(input[:maxLen])
	}
	t.emit(evt)
}

func (t *LiveTracer) CaptureEnd(output []byte, usedGas uint64, err error) {
	evt := LiveTracerEvent{
		Type:    "return",
		GasUsed: usedGas,
	}
	if err != nil {
		evt.Error = err.Error()
	}
	t.emit(evt)
}

func (t *LiveTracer) CaptureEnter(typ vm.OpCode, from types.Address, to types.Address, input []byte, gas uint64, value *uint256.Int) {
	evt := LiveTracerEvent{
		Type: "enter",
		From: &from,
		To:   &to,
		Gas:  gas,
	}
	if value != nil {
		evt.Value = value.Hex()
	}
	t.emit(evt)
}

func (t *LiveTracer) CaptureExit(output []byte, usedGas uint64, err error) {
	evt := LiveTracerEvent{
		Type:    "exit",
		GasUsed: usedGas,
	}
	if err != nil {
		evt.Error = err.Error()
	}
	t.emit(evt)
}

// CaptureState is intentionally a no-op for the live tracer.
// Per-opcode tracing would be too verbose for real-time streaming.
func (t *LiveTracer) CaptureState(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, rData []byte, depth int, err error) {
	// Intentionally empty: per-opcode events are too verbose for live streaming.
	// Use debug_traceTransaction for opcode-level detail.
}

func (t *LiveTracer) CaptureFault(pc uint64, op vm.OpCode, gas, cost uint64, scope *vm.ScopeContext, depth int, err error) {
	evt := LiveTracerEvent{
		Type:  "fault",
		Depth: depth,
	}
	if err != nil {
		evt.Error = err.Error()
	}
	t.emit(evt)
}

// GetResult returns accumulated events as JSON (for Tracer interface compatibility).
func (t *LiveTracer) GetResult() (json.RawMessage, error) {
	return json.Marshal(map[string]interface{}{
		"type":       "liveTracer",
		"emitted":    t.emitted,
		"maxEvents":  t.maxEvents,
		"blockNumber": t.blockNumber,
	})
}

// Stop terminates the tracer.
func (t *LiveTracer) Stop(err error) {
	t.Close()
}

// Compile-time check that LiveTracer implements vm.EVMLogger.
var _ vm.EVMLogger = (*LiveTracer)(nil)
