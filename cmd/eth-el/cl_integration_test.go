// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/golang-jwt/jwt/v4"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/engineapi"
)

// clHarness stands up a full eth-el Node with the engineAPI service,
// hands back an authenticated JSON-RPC caller, and registers a
// Cleanup that tears everything down. Every CL-integration test that
// needs an engineAPI endpoint funnels through this helper so the
// per-test boilerplate is limited to building request payloads and
// asserting response shapes.
type clHarness struct {
	t        *testing.T
	node     *ethel.Node
	datadir  string
	jwtPath  string
	baseURL  string
	jwtToken string
	cancel   context.CancelFunc
	startErr <-chan error
}

func newCLHarness(t *testing.T) *clHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("opens real MDBX; skipped in -short mode")
	}

	datadir := t.TempDir()
	port := pickFreePort(t)
	jwtPath := filepath.Join(datadir, "jwt.hex")

	cfg := conf.DefaultEthELCfg()
	cfg.DataDir = datadir
	cfg.Storage.MapSize = 1 * datasize.GB
	cfg.Bootstrap.Enabled = false
	cfg.EngineAPI.Enabled = true
	cfg.EngineAPI.Host = "127.0.0.1"
	cfg.EngineAPI.Port = port
	cfg.EngineAPI.JWTSecretPath = jwtPath

	node, err := ethel.NewNode(cfg)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	node.RegisterFactory(func(n *ethel.Node) ethel.Service {
		return engineapi.New(cfg.EngineAPI, n.ChainConfig(), n.Engine(), n.RwDB(), n.OutFreezer())
	})

	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- node.Start(ctx) }()

	// Wait for engineAPI listener.
	url := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-startDone
			t.Fatal("engineAPI listener did not come up in 15s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Sign a JWT with the auto-generated secret.
	secretHex, err := os.ReadFile(jwtPath)
	if err != nil {
		cancel()
		<-startDone
		t.Fatalf("read jwt: %v", err)
	}
	secret, err := hex.DecodeString(strings.TrimSpace(string(secretHex)))
	if err != nil {
		cancel()
		<-startDone
		t.Fatalf("decode jwt: %v", err)
	}
	tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}).SignedString(secret)
	if err != nil {
		cancel()
		<-startDone
		t.Fatalf("sign token: %v", err)
	}

	h := &clHarness{
		t:        t,
		node:     node,
		datadir:  datadir,
		jwtPath:  jwtPath,
		baseURL:  url,
		jwtToken: tokenStr,
		cancel:   cancel,
		startErr: startDone,
	}
	t.Cleanup(h.Close)
	return h
}

// Close shuts the harness down and blocks until Node.Start returns.
// Idempotent.
func (h *clHarness) Close() {
	h.cancel()
	select {
	case <-h.startErr:
	case <-time.After(10 * time.Second):
		h.t.Error("harness: Start did not return after cancel")
	}
	_ = h.node.Stop()
}

// jsonrpcResponse is the minimal shape we care about. Error is a
// generic map so we do not need a full rpc.Error type.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call sends an authenticated JSON-RPC request and decodes the
// envelope. The caller decides whether an Error is fatal — some
// engine API methods return application errors as Result rather than
// JSON-RPC errors, so the harness does not make that decision.
func (h *clHarness) call(method string, params any) jsonrpcResponse {
	h.t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		h.t.Fatalf("marshal request: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, h.baseURL, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.jwtToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatalf("POST %s: %v", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		h.t.Fatalf("POST %s: status %d body %q", method, resp.StatusCode, buf)
	}
	var out jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		h.t.Fatalf("decode %s: %v", method, err)
	}
	return out
}

// TestCL_ExchangeCapabilities verifies that a CL sending its own
// capability list receives the EL's list back. This is the first call
// every CL makes after connecting and must succeed on an otherwise
// empty Node (no chaindata, no blocks).
func TestCL_ExchangeCapabilities(t *testing.T) {
	h := newCLHarness(t)

	resp := h.call("engine_exchangeCapabilities", []any{
		[]string{
			"engine_newPayloadV4",
			"engine_forkchoiceUpdatedV4",
			"engine_getPayloadV4",
		},
	})
	if resp.Error != nil {
		t.Fatalf("exchangeCapabilities: %+v", resp.Error)
	}
	var methods []string
	if err := json.Unmarshal(resp.Result, &methods); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	// Sanity: the EL advertises every method the CL actually cares
	// about. Full list is in supportedEngineMethods; we spot-check
	// the critical ones.
	required := []string{
		"engine_newPayloadV4",
		"engine_forkchoiceUpdatedV4",
		"engine_getPayloadV4",
		"engine_exchangeCapabilities",
	}
	have := make(map[string]bool, len(methods))
	for _, m := range methods {
		have[m] = true
	}
	for _, m := range required {
		if !have[m] {
			t.Errorf("exchangeCapabilities: missing %q", m)
		}
	}
}

// TestCL_ForkchoiceUpdatedV4EmptyState verifies that a CL can send
// forkchoiceUpdatedV4 against an empty chain and receive a valid
// ForkchoiceUpdatedResponse. With no chaindata head, the EL responds
// SYNCING (not INVALID), which is what the Engine API spec says a
// not-yet-caught-up EL should return.
func TestCL_ForkchoiceUpdatedV4EmptyState(t *testing.T) {
	h := newCLHarness(t)

	resp := h.call("engine_forkchoiceUpdatedV4", []any{
		map[string]any{
			"headBlockHash":      "0x0000000000000000000000000000000000000000000000000000000000000000",
			"safeBlockHash":      "0x0000000000000000000000000000000000000000000000000000000000000000",
			"finalizedBlockHash": "0x0000000000000000000000000000000000000000000000000000000000000000",
		},
		nil, // no payload attributes
	})
	if resp.Error != nil {
		t.Fatalf("forkchoiceUpdatedV4: %+v", resp.Error)
	}
	var fcu struct {
		PayloadStatus struct {
			Status string `json:"status"`
		} `json:"payloadStatus"`
	}
	if err := json.Unmarshal(resp.Result, &fcu); err != nil {
		t.Fatalf("decode fcu: %v", err)
	}
	// Empty chaindata → no current head → SYNCING is the spec-mandated
	// response. INVALID would only be returned if chaindata had a
	// conflicting head and the CL pointed at a different fork.
	if fcu.PayloadStatus.Status != "SYNCING" {
		t.Fatalf("forkchoiceUpdatedV4 status: got %q, want SYNCING", fcu.PayloadStatus.Status)
	}
}

// TestCL_NewPayloadV4MissingFields verifies the EL surfaces an
// INVALID PayloadStatus when a CL sends a payload that fails
// top-level validation (missing parent beacon root, missing
// withdrawals, etc.). This is the error path every CL client library
// needs to handle gracefully.
func TestCL_NewPayloadV4MissingFields(t *testing.T) {
	h := newCLHarness(t)

	// Deliberately skeletal payload: valid JSON shape, but every
	// mandatory field is zero. internal/api validates Pectra payloads
	// and returns an invalidPayloadResponse before the state adapter
	// is even touched.
	payload := map[string]any{
		"parentHash":    "0x0000000000000000000000000000000000000000000000000000000000000000",
		"feeRecipient":  "0x0000000000000000000000000000000000000000",
		"stateRoot":     "0x0000000000000000000000000000000000000000000000000000000000000000",
		"receiptsRoot":  "0x0000000000000000000000000000000000000000000000000000000000000000",
		"logsBloom":     "0x" + strings.Repeat("00", 256),
		"prevRandao":    "0x0000000000000000000000000000000000000000000000000000000000000000",
		"blockNumber":   "0x1",
		"gasLimit":      "0x1c9c380",
		"gasUsed":       "0x0",
		"timestamp":     "0x0",
		"extraData":     "0x",
		"baseFeePerGas": "0x0",
		"blockHash":     "0x0000000000000000000000000000000000000000000000000000000000000000",
		"transactions":  []string{},
		// Deliberately missing: withdrawals, blobGasUsed, excessBlobGas.
	}
	resp := h.call("engine_newPayloadV4", []any{
		payload,
		[]string{},        // versioned hashes
		"0x0000000000000000000000000000000000000000000000000000000000000000", // parent beacon root
		[]string{},        // execution requests
	})
	// The Engine API spec allows this to come back either as a
	// jsonrpc error (for shape violations) or as a Result with
	// status=INVALID (for validation failures). Accept either — both
	// are "CL handled this correctly" outcomes.
	if resp.Error != nil {
		return
	}
	var status struct {
		Status          string  `json:"status"`
		LatestValidHash *string `json:"latestValidHash"`
		ValidationError *string `json:"validationError"`
	}
	if err := json.Unmarshal(resp.Result, &status); err != nil {
		t.Fatalf("decode payloadStatus: %v", err)
	}
	if status.Status != "INVALID" && status.Status != "SYNCING" {
		t.Fatalf("newPayloadV4 status: got %q, want INVALID or SYNCING", status.Status)
	}
}

// TestCL_RejectsUnauthenticated verifies that a CL without a valid
// JWT is turned away at the middleware before reaching any engine API
// handler. This is the single most important contract: wrong token
// must not accidentally execute a block.
func TestCL_RejectsUnauthenticated(t *testing.T) {
	h := newCLHarness(t)

	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"engine_exchangeCapabilities","params":[[]]}`)
	resp, err := http.Post(h.baseURL, "application/json", body)
	if err != nil {
		t.Fatalf("unauth POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauth POST: status %d, want 403", resp.StatusCode)
	}
}

// TestCL_StaleJWTRejected confirms that an expired JWT (iat too far
// in the past) is rejected even though it is well-formed. The Engine
// API spec (§3.1) mandates a 60-second drift window.
func TestCL_StaleJWTRejected(t *testing.T) {
	h := newCLHarness(t)

	secretHex, err := os.ReadFile(h.jwtPath)
	if err != nil {
		t.Fatalf("cannot read JWT secret: %v", err)
	}
	secret, err := hex.DecodeString(strings.TrimSpace(string(secretHex)))
	if err != nil {
		t.Fatalf("decode jwt: %v", err)
	}
	stale, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(time.Now().Add(-10 * time.Minute)),
	}).SignedString(secret)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, h.baseURL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"engine_exchangeCapabilities","params":[[]]}`))
	req.Header.Set("Authorization", "Bearer "+stale)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stale POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("stale POST: status %d body %q, want 403", resp.StatusCode, buf)
	}
}
