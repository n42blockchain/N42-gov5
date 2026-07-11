// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// capabilityGate is the rpccaps front door: it inspects each JSON-RPC request's
// method and rejects the ones this node's data mode cannot serve, so Blockscout
// (or any client) gets a clean JSON-RPC error instead of a wrong/opaque result
// from a handler reading data the mode does not retain. Only the eth_ methods
// rpccaps knows about are gated; every other namespace (web3/net/debug/trace)
// passes through untouched. Scope is checked as Latest (a permissive default);
// historical-scope gating for the Full mode is a known follow-up.

package publicrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

// maxGateBody bounds how much of a request body the gate will buffer to peek the
// method(s); larger bodies pass through ungated rather than being rejected.
const maxGateBody = 8 << 20 // 8 MiB

// gatedMethods is the set of eth_ methods rpccaps has an opinion about. Methods
// outside it (web3/net/debug/trace, and eth_ methods not in the matrix) are
// never gated.
var gatedMethods = func() map[string]bool {
	m := make(map[string]bool)
	for _, k := range rpccaps.Methods() {
		m[k] = true
	}
	return m
}()

type capabilityGate struct {
	mode rpccaps.Mode
	// head returns the current canonical head number, used to classify a
	// request's block argument as Latest vs Historical. nil → everything Latest.
	head func() uint64
	next http.Handler
}

func newCapabilityGate(mode rpccaps.Mode, head func() uint64, next http.Handler) http.Handler {
	return &capabilityGate{mode: mode, head: head, next: next}
}

type rpcReq struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params json.RawMessage `json:"params"`
}

func (g *capabilityGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.Body == nil {
		g.next.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxGateBody))
	r.Body.Close()
	if err != nil {
		// Can't peek — let the rpc server return its own parse error.
		r.Body = io.NopCloser(bytes.NewReader(body))
		g.next.ServeHTTP(w, r)
		return
	}
	// Restore the body for the downstream rpc server.
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))

	if method, id, ok := g.firstUnsupported(body); ok {
		writeNotAvailable(w, id, method, g.mode)
		return
	}
	g.next.ServeHTTP(w, r)
}

// firstUnsupported returns the first request method the mode cannot serve (with
// its id) across a single or batch request.
func (g *capabilityGate) firstUnsupported(body []byte) (string, json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return "", nil, false
	}
	if trimmed[0] == '[' {
		var arr []rpcReq
		if json.Unmarshal(trimmed, &arr) != nil {
			return "", nil, false
		}
		for _, req := range arr {
			if g.rejects(req.Method, req.Params) {
				return req.Method, req.ID, true
			}
		}
		return "", nil, false
	}
	var req rpcReq
	if json.Unmarshal(trimmed, &req) != nil {
		return "", nil, false
	}
	if g.rejects(req.Method, req.Params) {
		return req.Method, req.ID, true
	}
	return "", nil, false
}

func (g *capabilityGate) rejects(method string, params json.RawMessage) bool {
	if !gatedMethods[method] {
		return false
	}
	scope := g.scopeOf(method, params)
	return rpccaps.Serviceable(g.mode, method, scope) == rpccaps.No
}

// scopeOf classifies a request as Latest or Historical from its block argument.
// A concrete block number strictly below the head is Historical; latest/pending/
// safe/finalized tags, block-hash forms, unknown params, or a missing head
// provider are Latest (the permissive default).
func (g *capabilityGate) scopeOf(method string, params json.RawMessage) rpccaps.Scope {
	if g.head == nil {
		return rpccaps.Latest
	}
	bn, ok := blockNumberArg(method, params)
	if !ok {
		return rpccaps.Latest
	}
	if bn < g.head() {
		return rpccaps.Historical
	}
	return rpccaps.Latest
}

// blockParamPos maps a method to the positional index of its blockNrOrHash
// argument. Methods absent from the map (or object-shaped like eth_getLogs) are
// handled separately; -1 means "no positional block arg".
var blockParamPos = map[string]int{
	"eth_getBalance":          1,
	"eth_getCode":             1,
	"eth_getTransactionCount": 1,
	"eth_getStorageAt":        2,
	"eth_call":                1,
	"eth_estimateGas":         1,
	"eth_getProof":            2,
	"eth_getBlockByNumber":    0,
	"eth_getBlockReceipts":    0,
}

// blockNumberArg extracts a concrete block number for scope classification.
// ok=false for tags, hashes, or methods whose scope can't be read positionally.
func blockNumberArg(method string, params json.RawMessage) (uint64, bool) {
	if method == "eth_getLogs" || method == "eth_newFilter" {
		return fromBlockOfFilter(params)
	}
	idx, known := blockParamPos[method]
	if !known {
		return 0, false
	}
	var arr []json.RawMessage
	if json.Unmarshal(params, &arr) != nil || idx >= len(arr) {
		return 0, false
	}
	return parseBlockArg(arr[idx])
}

// fromBlockOfFilter reads the fromBlock of a filter-object first param.
func fromBlockOfFilter(params json.RawMessage) (uint64, bool) {
	var arr []json.RawMessage
	if json.Unmarshal(params, &arr) != nil || len(arr) == 0 {
		return 0, false
	}
	var f struct {
		FromBlock string `json:"fromBlock"`
	}
	if json.Unmarshal(arr[0], &f) != nil {
		return 0, false
	}
	return parseBlockArg(json.RawMessage(strconv.Quote(f.FromBlock)))
}

// parseBlockArg parses a JSON block argument: a "0x…" number, "earliest" (→0),
// or a {blockNumber:"0x…"} object. Tags latest/pending/safe/finalized and
// block-hash forms return ok=false (treated as Latest).
func parseBlockArg(raw json.RawMessage) (uint64, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return 0, false
	}
	if s[0] == '"' {
		var tag string
		if json.Unmarshal(raw, &tag) != nil {
			return 0, false
		}
		return parseBlockTag(tag)
	}
	if s[0] == '{' {
		var o struct {
			BlockNumber string `json:"blockNumber"`
		}
		if json.Unmarshal(raw, &o) != nil || o.BlockNumber == "" {
			return 0, false // {blockHash:…} or empty → Latest
		}
		return parseBlockTag(o.BlockNumber)
	}
	return 0, false
}

func parseBlockTag(tag string) (uint64, bool) {
	switch tag {
	case "latest", "pending", "safe", "finalized":
		return 0, false
	case "earliest":
		return 0, true
	}
	n, err := strconv.ParseUint(strings.TrimPrefix(tag, "0x"), 16, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// writeNotAvailable emits a JSON-RPC 2.0 error (-32601) explaining the method is
// not available in this data mode.
func writeNotAvailable(w http.ResponseWriter, id json.RawMessage, method string, mode rpccaps.Mode) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	msg := fmt.Sprintf("method %s not available in %s mode", method, mode.String())
	resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":%q}}`, id, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, resp)
}
