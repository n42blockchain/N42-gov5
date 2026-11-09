// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package packetd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// RPCError is a JSON-RPC error object returned by the node.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// IsMethodNotFound reports whether err is the node refusing an unknown or
// disabled method (-32601, reth's answer for a namespace it does not serve).
func IsMethodNotFound(err error) bool {
	var re *RPCError
	return errors.As(err, &re) && re.Code == -32601
}

// Client is a minimal JSON-RPC 2.0 client over HTTP with batch support.
type Client struct {
	URL  string
	HTTP *http.Client

	ids   atomic.Uint64
	calls atomic.Uint64 // requests sent (a batch counts once)
}

// NewClient returns a client for url with a per-request timeout.
func NewClient(url string, timeout time.Duration) *Client {
	return &Client{URL: url, HTTP: &http.Client{Timeout: timeout}}
}

// Requests returns the number of HTTP requests sent so far.
func (c *Client) Requests() uint64 { return c.calls.Load() }

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *RPCError       `json:"error"`
}

// BatchElem is one call of a batch; Result is decoded into, Err is set per call.
type BatchElem struct {
	Method string
	Params []any
	Result any
	Err    error
}

// Call performs one JSON-RPC call and decodes the result into out (which may
// be nil). A JSON null result decodes as the zero value.
func (c *Client) Call(ctx context.Context, out any, method string, params ...any) error {
	b := []BatchElem{{Method: method, Params: params, Result: out}}
	if err := c.post(ctx, b, false); err != nil {
		return err
	}
	return b[0].Err
}

// Batch sends the calls in one HTTP request. The returned error is a
// transport error; per-call errors are in each element's Err.
func (c *Client) Batch(ctx context.Context, b []BatchElem) error {
	if len(b) == 0 {
		return nil
	}
	return c.post(ctx, b, true)
}

func (c *Client) post(ctx context.Context, b []BatchElem, batch bool) error {
	reqs := make([]rpcRequest, len(b))
	byID := make(map[uint64]int, len(b))
	for i := range b {
		if b[i].Params == nil {
			b[i].Params = []any{}
		}
		id := c.ids.Add(1)
		reqs[i] = rpcRequest{JSONRPC: "2.0", ID: id, Method: b[i].Method, Params: b[i].Params}
		byID[id] = i
	}
	var body []byte
	var err error
	if batch {
		body, err = json.Marshal(reqs)
	} else {
		body, err = json.Marshal(reqs[0])
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.calls.Add(1)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rpc %s: HTTP %d: %s", b[0].Method, resp.StatusCode, bytes.TrimSpace(raw))
	}
	var resps []rpcResponse
	if batch {
		if err := json.Unmarshal(raw, &resps); err != nil {
			// A server that rejects the whole batch answers with one object.
			var one rpcResponse
			if json.Unmarshal(raw, &one) == nil && one.Error != nil {
				return one.Error
			}
			return fmt.Errorf("rpc batch: decode response: %w", err)
		}
	} else {
		var one rpcResponse
		if err := json.Unmarshal(raw, &one); err != nil {
			return fmt.Errorf("rpc %s: decode response: %w", b[0].Method, err)
		}
		resps = []rpcResponse{one}
	}
	seen := make([]bool, len(b))
	for _, r := range resps {
		i, ok := byID[r.ID]
		if !ok || seen[i] {
			continue
		}
		seen[i] = true
		switch {
		case r.Error != nil:
			b[i].Err = r.Error
		case b[i].Result != nil && len(r.Result) > 0:
			if err := json.Unmarshal(r.Result, b[i].Result); err != nil {
				b[i].Err = fmt.Errorf("rpc %s: decode result: %w", b[i].Method, err)
			}
		}
	}
	for i := range b {
		if !seen[i] {
			b[i].Err = fmt.Errorf("rpc %s: no response", b[i].Method)
		}
	}
	return nil
}
