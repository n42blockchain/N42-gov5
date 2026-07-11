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
// Parity / OpenEthereum "trace_" JSON-RPC namespace. Blockscout's Erigon
// variant (ETHEREUM_JSONRPC_VARIANT=erigon) indexes internal transactions and
// block rewards through trace_block / trace_replayBlockTransactions, and tools
// like Otterscan / eth debug clients use trace_transaction / trace_filter.
//
// The heavy lifting is already done by the flatCallTracer (native/call_flat.go),
// which emits exactly the Parity flat-trace object — action / result /
// traceAddress / subtraces / type plus blockHash / blockNumber /
// transactionHash / transactionPosition. This file is the RPC surface that
// drives that tracer per transaction and formats the aggregate the way the
// trace_ methods return it (a single flat array for trace_block, the
// {output, trace, stateDiff, vmTrace} envelope for the replay variants).

package tracers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/internal/api"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"

	common "github.com/n42blockchain/N42/common/types"
)

// maxTraceFilterBlocks bounds trace_filter's block range so a single call can
// not force the node to re-execute an unbounded slice of chain history.
const maxTraceFilterBlocks = 1000

// flatCallTracerName is the native tracer that produces Parity flat traces.
const flatCallTracerName = "flatCallTracer"

// TraceAPI exposes the Parity/Erigon-style `trace_` namespace on top of the
// geth-style tracing API. It holds an *API so it can reuse the exact block /
// transaction replay path (state-at-block, per-tx context) that debug_ uses.
type TraceAPI struct {
	api *API
}

// NewTraceAPI builds the trace_ service from the same backend as the debug
// tracer, so both share one state-access implementation.
func NewTraceAPI(backend Backend) *TraceAPI {
	return &TraceAPI{api: NewAPI(backend)}
}

// flatTraceConfig returns the TraceConfig that selects the flatCallTracer with
// Parity-compatible error strings (Blockscout matches on them).
func flatTraceConfig() *TraceConfig {
	name := flatCallTracerName
	return &TraceConfig{
		Tracer:       &name,
		TracerConfig: json.RawMessage(`{"convertParityErrors":true}`),
	}
}

// rawTraceArray coerces a flatCallTracer result (a json.RawMessage holding a
// JSON array of flat frames) into a slice of raw frames so callers can
// concatenate, filter, or index into it.
func rawTraceArray(v interface{}) ([]json.RawMessage, error) {
	if v == nil {
		return nil, nil
	}
	raw, ok := v.(json.RawMessage)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("trace: expected a flat-trace array: %w", err)
	}
	return arr, nil
}

// flattenBlock concatenates every transaction's flat-trace array into the
// single array trace_block / trace_filter return. Transactions whose trace
// failed are skipped (Parity omits them rather than aborting the block).
func flattenBlock(results []*txTraceResult) ([]json.RawMessage, error) {
	out := []json.RawMessage{}
	for _, r := range results {
		if r == nil || r.Error != "" {
			continue
		}
		frames, err := rawTraceArray(r.Result)
		if err != nil {
			return nil, err
		}
		out = append(out, frames...)
	}
	return out, nil
}

// Block implements trace_block: all flat call traces for every transaction in
// the given block, in a single array. This is Blockscout's Erigon-variant
// internal-transaction source.
func (t *TraceAPI) Block(ctx context.Context, number rpc.BlockNumber) ([]json.RawMessage, error) {
	results, err := t.api.TraceBlockByNumber(ctx, number, flatTraceConfig())
	if err != nil {
		return nil, err
	}
	return flattenBlock(results)
}

// Transaction implements trace_transaction: the flat call traces for one
// transaction (its top-level call and every nested call/create/selfdestruct).
func (t *TraceAPI) Transaction(ctx context.Context, hash common.Hash) ([]json.RawMessage, error) {
	res, err := t.api.TraceTransaction(ctx, hash, flatTraceConfig())
	if err != nil {
		return nil, err
	}
	return rawTraceArray(res)
}

// Get implements trace_get: the single trace at the given nested trace-address
// path within a transaction (Parity indexes the flat array by walking
// traceAddress). indices is the traceAddress to select.
func (t *TraceAPI) Get(ctx context.Context, hash common.Hash, indices []hexutil.Uint) (json.RawMessage, error) {
	frames, err := t.Transaction(ctx, hash)
	if err != nil {
		return nil, err
	}
	want := make([]int, len(indices))
	for i, ix := range indices {
		want[i] = int(ix)
	}
	for _, f := range frames {
		var lite struct {
			TraceAddress []int `json:"traceAddress"`
		}
		if err := json.Unmarshal(f, &lite); err != nil {
			return nil, err
		}
		if intSliceEqual(lite.TraceAddress, want) {
			return f, nil
		}
	}
	return nil, nil
}

// replayResult is the Parity trace_replay* envelope. Only "trace" is fully
// supported; "stateDiff" and "vmTrace" are returned as null unless a future
// change wires the prestate/vmtrace tracers into the Parity formats. Blockscout
// only requests "trace" for internal-transaction indexing.
type replayResult struct {
	Output          hexutil.Bytes     `json:"output"`
	StateDiff       interface{}       `json:"stateDiff"`
	Trace           []json.RawMessage `json:"trace"`
	VMTrace         interface{}       `json:"vmTrace"`
	TransactionHash *common.Hash      `json:"transactionHash,omitempty"`
}

// traceTypeWanted reports whether traceTypes requests a given Parity trace type.
// An empty list defaults to just "trace" (what Blockscout sends).
func traceTypeWanted(traceTypes []string, want string) bool {
	if len(traceTypes) == 0 {
		return want == "trace"
	}
	for _, tt := range traceTypes {
		if tt == want {
			return true
		}
	}
	return false
}

// topLevelOutput extracts the transaction's return data (call output or created
// code) from the root flat frame (the one whose traceAddress is empty).
func topLevelOutput(frames []json.RawMessage) hexutil.Bytes {
	for _, f := range frames {
		var lite struct {
			TraceAddress []int `json:"traceAddress"`
			Result       struct {
				Output *hexutil.Bytes `json:"output"`
				Code   *hexutil.Bytes `json:"code"`
			} `json:"result"`
		}
		if err := json.Unmarshal(f, &lite); err != nil {
			continue
		}
		if len(lite.TraceAddress) != 0 {
			continue
		}
		if lite.Result.Output != nil {
			return *lite.Result.Output
		}
		if lite.Result.Code != nil {
			return *lite.Result.Code
		}
		return hexutil.Bytes{}
	}
	return hexutil.Bytes{}
}

// buildReplay assembles the Parity replay envelope from a transaction's flat
// frames. txHash is set only for the block-level variant.
func buildReplay(frames []json.RawMessage, traceTypes []string, txHash *common.Hash) *replayResult {
	r := &replayResult{
		Output:          topLevelOutput(frames),
		StateDiff:       nil,
		Trace:           []json.RawMessage{},
		VMTrace:         nil,
		TransactionHash: txHash,
	}
	if traceTypeWanted(traceTypes, "trace") {
		r.Trace = frames
	}
	return r
}

// ReplayTransaction implements trace_replayTransaction.
func (t *TraceAPI) ReplayTransaction(ctx context.Context, hash common.Hash, traceTypes []string) (*replayResult, error) {
	frames, err := t.Transaction(ctx, hash)
	if err != nil {
		return nil, err
	}
	return buildReplay(frames, traceTypes, nil), nil
}

// ReplayBlockTransactions implements trace_replayBlockTransactions: one replay
// envelope per transaction, each tagged with its transactionHash. This is the
// method Blockscout's Erigon variant uses for internal transactions and block
// reward indexing.
func (t *TraceAPI) ReplayBlockTransactions(ctx context.Context, number rpc.BlockNumber, traceTypes []string) ([]*replayResult, error) {
	block, err := t.api.blockByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	results, err := t.api.TraceBlockByNumber(ctx, number, flatTraceConfig())
	if err != nil {
		return nil, err
	}
	txs := block.Transactions()
	out := make([]*replayResult, 0, len(results))
	for i, r := range results {
		if r == nil || r.Error != "" {
			out = append(out, &replayResult{Trace: []json.RawMessage{}})
			continue
		}
		frames, err := rawTraceArray(r.Result)
		if err != nil {
			return nil, err
		}
		var txHash *common.Hash
		if i < len(txs) {
			h := txs[i].Hash()
			txHash = &h
		}
		out = append(out, buildReplay(frames, traceTypes, txHash))
	}
	return out, nil
}

// Call implements trace_call: trace a message call on top of a block's state
// (like eth_call, but returning the Parity replay envelope). blockNrOrHash is
// optional; nil defaults to the latest block.
func (t *TraceAPI) Call(ctx context.Context, args api.TransactionArgs, traceTypes []string, blockNrOrHash *rpc.BlockNumberOrHash) (*replayResult, error) {
	bnh := rpc.BlockNumberOrHashWithNumber(rpc.LatestBlockNumber)
	if blockNrOrHash != nil {
		bnh = *blockNrOrHash
	}
	name := flatCallTracerName
	cfg := &TraceCallConfig{
		TraceConfig: TraceConfig{
			Tracer:       &name,
			TracerConfig: json.RawMessage(`{"convertParityErrors":true}`),
		},
	}
	res, err := t.api.TraceCall(ctx, args, bnh, cfg)
	if err != nil {
		return nil, err
	}
	frames, err := rawTraceArray(res)
	if err != nil {
		return nil, err
	}
	return buildReplay(frames, traceTypes, nil), nil
}

// TraceFilterRequest is the trace_filter query: a block range plus optional
// from/to address sets and after/count pagination.
type TraceFilterRequest struct {
	FromBlock   *rpc.BlockNumber `json:"fromBlock"`
	ToBlock     *rpc.BlockNumber `json:"toBlock"`
	FromAddress []common.Address `json:"fromAddress"`
	ToAddress   []common.Address `json:"toAddress"`
	After       *uint64          `json:"after"`
	Count       *uint64          `json:"count"`
}

// Filter implements trace_filter: all flat traces across a block range whose
// action.from / action.to match the requested address sets, with after/count
// pagination. Blockscout uses this for address-scoped internal-transaction
// indexing.
func (t *TraceAPI) Filter(ctx context.Context, req TraceFilterRequest) ([]json.RawMessage, error) {
	from := uint64(0)
	if req.FromBlock != nil && *req.FromBlock >= 0 {
		from = uint64(req.FromBlock.Int64())
	}
	latest, err := t.api.blockByNumber(ctx, rpc.LatestBlockNumber)
	if err != nil {
		return nil, err
	}
	latestNum, err := requireBlockNumber(latest, "latest block number unavailable")
	if err != nil {
		return nil, err
	}
	to := latestNum.Uint64()
	if req.ToBlock != nil && *req.ToBlock >= 0 {
		to = uint64(req.ToBlock.Int64())
	}
	if to < from {
		return []json.RawMessage{}, nil
	}
	if to-from+1 > maxTraceFilterBlocks {
		return nil, fmt.Errorf("trace_filter: block range %d too large (max %d)", to-from+1, maxTraceFilterBlocks)
	}

	fromSet := addressSet(req.FromAddress)
	toSet := addressSet(req.ToAddress)

	var (
		out     = []json.RawMessage{}
		skipped uint64
		emitted uint64
	)
	for n := from; n <= to; n++ {
		frames, err := t.Block(ctx, rpc.BlockNumber(n))
		if err != nil {
			return nil, err
		}
		for _, f := range frames {
			if !frameMatchesAddresses(f, fromSet, toSet) {
				continue
			}
			if req.After != nil && skipped < *req.After {
				skipped++
				continue
			}
			out = append(out, f)
			emitted++
			if req.Count != nil && emitted >= *req.Count {
				return out, nil
			}
		}
	}
	return out, nil
}

// addressSet turns an address list into a lookup set; nil/empty means "match
// any" (represented by a nil map so callers can distinguish it).
func addressSet(addrs []common.Address) map[common.Address]struct{} {
	if len(addrs) == 0 {
		return nil
	}
	m := make(map[common.Address]struct{}, len(addrs))
	for _, a := range addrs {
		m[a] = struct{}{}
	}
	return m
}

// frameMatchesAddresses reports whether a flat frame's action.from / action.to
// satisfy the (optional) from/to filters. A nil filter matches any value.
func frameMatchesAddresses(frame json.RawMessage, fromSet, toSet map[common.Address]struct{}) bool {
	if fromSet == nil && toSet == nil {
		return true
	}
	var lite struct {
		Action struct {
			From *common.Address `json:"from"`
			To   *common.Address `json:"to"`
		} `json:"action"`
	}
	if err := json.Unmarshal(frame, &lite); err != nil {
		return false
	}
	if fromSet != nil {
		if lite.Action.From == nil {
			return false
		}
		if _, ok := fromSet[*lite.Action.From]; !ok {
			return false
		}
	}
	if toSet != nil {
		if lite.Action.To == nil {
			return false
		}
		if _, ok := toSet[*lite.Action.To]; !ok {
			return false
		}
	}
	return true
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
