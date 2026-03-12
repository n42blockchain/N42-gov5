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

// Package mcp implements a Model Context Protocol (MCP) server for N42,
// enabling AI agents to query and analyze on-chain data through a
// standardized JSON-RPC interface.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

const (
	// maxRequestBody is the maximum allowed JSON-RPC request body size (1 MB).
	maxRequestBody = 1 << 20

	// protocolVersion is the MCP protocol version this server implements.
	protocolVersion = "2024-11-05"

	// serverName identifies this MCP server implementation.
	serverName = "n42-mcp"

	// defaultReadTimeout is the HTTP server read timeout.
	defaultReadTimeout = 30 * time.Second

	// defaultWriteTimeout is the HTTP server write timeout.
	defaultWriteTimeout = 30 * time.Second

	// defaultIdleTimeout is the HTTP server idle timeout.
	defaultIdleTimeout = 120 * time.Second
)

// MCP JSON-RPC error codes.
const (
	errCodeParse          = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternal       = -32603
	errCodeToolNotFound   = -32001
	errCodeResourceNotFound = -32002
)

// Backend provides access to the N42 blockchain state required by the MCP
// server. Implementations must be safe for concurrent use.
type Backend interface {
	BlockChain() common.IBlockChain
	Database() kv.RwDB
	TxPool() common.ITxsPool
	SyncProgress() *SyncStatus
	PeerCount() int
}

// SyncStatus describes the current synchronization state of the node.
type SyncStatus struct {
	CurrentBlock uint64 `json:"current_block"`
	HighestBlock uint64 `json:"highest_block"`
	Syncing      bool   `json:"syncing"`
}

// Tool describes an MCP tool that AI agents can invoke.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Handler     ToolHandler     `json:"-"`
}

// ToolHandler is the function signature for MCP tool implementations.
type ToolHandler func(ctx context.Context, params json.RawMessage) (interface{}, error)

// ResourceProvider describes a read-only MCP resource.
type ResourceProvider struct {
	Name        string                                   `json:"name"`
	Description string                                   `json:"description"`
	Handler     func(ctx context.Context) (interface{}, error) `json:"-"`
}

// MCPRequest is a JSON-RPC 2.0 request envelope for MCP.
type MCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// MCPResponse is a JSON-RPC 2.0 response envelope for MCP.
type MCPResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *MCPError   `json:"error,omitempty"`
}

// MCPError represents a JSON-RPC 2.0 error object.
type MCPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server is the MCP HTTP server. It exposes blockchain data and operations
// as MCP tools and resources accessible to AI agents.
type Server struct {
	tools        map[string]Tool
	resources    map[string]ResourceProvider
	allowedTools map[string]struct{} // nil means all allowed
	backend      Backend
	listener     net.Listener
	httpServer   *http.Server
	mu           sync.RWMutex
	stopped      chan struct{}
}

// NewServer creates a new MCP server backed by the given blockchain backend.
// If allowedTools is non-empty, only the named tools will be registered.
func NewServer(backend Backend, allowedTools []string) *Server {
	s := &Server{
		tools:     make(map[string]Tool),
		resources: make(map[string]ResourceProvider),
		backend:   backend,
		stopped:   make(chan struct{}),
	}

	if len(allowedTools) > 0 {
		s.allowedTools = make(map[string]struct{}, len(allowedTools))
		for _, name := range allowedTools {
			s.allowedTools[name] = struct{}{}
		}
	}

	s.registerDefaultTools()
	s.registerDefaultResources()

	return s
}

// RegisterTool registers a tool with the server. If an allowlist is
// configured and the tool name is not in it, the tool is silently skipped.
func (s *Server) RegisterTool(t Tool) {
	if s.allowedTools != nil {
		if _, ok := s.allowedTools[t.Name]; !ok {
			return
		}
	}
	s.mu.Lock()
	s.tools[t.Name] = t
	s.mu.Unlock()
}

// RegisterResource registers a resource provider with the server.
func (s *Server) RegisterResource(r ResourceProvider) {
	s.mu.Lock()
	s.resources[r.Name] = r
	s.mu.Unlock()
}

// Start begins serving MCP requests on the given address (e.g. ":8553").
func (s *Server) Start(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("mcp: failed to listen on %s: %w", addr, err)
	}
	s.listener = ln

	mux := http.NewServeMux()
	mux.Handle("/", s)

	s.httpServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  defaultReadTimeout,
		WriteTimeout: defaultWriteTimeout,
		IdleTimeout:  defaultIdleTimeout,
	}

	go func() {
		log.Info("MCP server started", "addr", ln.Addr().String())
		if err := s.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("MCP server error", "err", err)
		}
		close(s.stopped)
	}()

	return nil
}

// Stop gracefully shuts down the MCP server. It applies a timeout to
// prevent blocking indefinitely if the server was never fully started.
func (s *Server) Stop() error {
	if s.httpServer == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	log.Info("MCP server stopping")
	err := s.httpServer.Shutdown(ctx)

	select {
	case <-s.stopped:
	case <-time.After(5 * time.Second):
		log.Warn("MCP server stop timed out waiting for serve goroutine")
	}

	return err
}

// ServeHTTP implements http.Handler. It accepts JSON-RPC 2.0 requests
// and dispatches them to the appropriate MCP method handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, nil, errCodeInvalidRequest, "only POST is supported")
		return
	}

	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		writeError(w, nil, errCodeInvalidRequest, "content-type must be application/json")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBody))
	if err != nil {
		writeError(w, nil, errCodeParse, "failed to read request body")
		return
	}

	var req MCPRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, nil, errCodeParse, "invalid JSON: "+err.Error())
		return
	}

	if req.JSONRPC != "2.0" {
		writeError(w, req.ID, errCodeInvalidRequest, "jsonrpc must be \"2.0\"")
		return
	}

	resp := s.handleRequest(r.Context(), &req)
	writeJSON(w, resp)
}

// handleRequest dispatches an MCP request to the correct protocol handler.
func (s *Server) handleRequest(ctx context.Context, req *MCPRequest) *MCPResponse {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolsCall(ctx, req)
	case "resources/list":
		return s.handleResourcesList(req)
	case "resources/read":
		return s.handleResourcesRead(ctx, req)
	default:
		return &MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: errCodeMethodNotFound, Message: "unknown method: " + req.Method},
		}
	}
}

// handleInitialize performs the MCP protocol handshake.
func (s *Server) handleInitialize(req *MCPRequest) *MCPResponse {
	s.mu.RLock()
	toolNames := make([]string, 0, len(s.tools))
	for name := range s.tools {
		toolNames = append(toolNames, name)
	}
	resourceNames := make([]string, 0, len(s.resources))
	for name := range s.resources {
		resourceNames = append(resourceNames, name)
	}
	s.mu.RUnlock()

	result := map[string]interface{}{
		"protocolVersion": protocolVersion,
		"serverInfo": map[string]interface{}{
			"name":    serverName,
			"version": "1.0.0",
		},
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{
				"list": true,
				"call": true,
			},
			"resources": map[string]interface{}{
				"list": true,
				"read": true,
			},
		},
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handleToolsList returns the list of available tools.
func (s *Server) handleToolsList(req *MCPRequest) *MCPResponse {
	s.mu.RLock()
	tools := make([]map[string]interface{}, 0, len(s.tools))
	for _, t := range s.tools {
		entry := map[string]interface{}{
			"name":        t.Name,
			"description": t.Description,
		}
		if t.Parameters != nil {
			entry["inputSchema"] = json.RawMessage(t.Parameters)
		}
		tools = append(tools, entry)
	}
	s.mu.RUnlock()

	return &MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"tools": tools},
	}
}

// handleToolsCall invokes a tool by name with the provided arguments.
func (s *Server) handleToolsCall(ctx context.Context, req *MCPRequest) *MCPResponse {
	var callParams struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &callParams); err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: errCodeInvalidParams, Message: "invalid tool call params: " + err.Error()},
		}
	}

	s.mu.RLock()
	tool, ok := s.tools[callParams.Name]
	s.mu.RUnlock()

	if !ok {
		return &MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: errCodeToolNotFound, Message: "tool not found: " + callParams.Name},
		}
	}

	result, err := tool.Handler(ctx, callParams.Arguments)
	if err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: errCodeInternal, Message: err.Error()},
		}
	}

	// Wrap result in MCP content format.
	content := []map[string]interface{}{
		{
			"type": "text",
			"text": mustMarshalString(result),
		},
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"content": content},
	}
}

// handleResourcesList returns the list of available resources.
func (s *Server) handleResourcesList(req *MCPRequest) *MCPResponse {
	s.mu.RLock()
	resources := make([]map[string]interface{}, 0, len(s.resources))
	for _, r := range s.resources {
		resources = append(resources, map[string]interface{}{
			"uri":         "n42://" + r.Name,
			"name":        r.Name,
			"description": r.Description,
			"mimeType":    "application/json",
		})
	}
	s.mu.RUnlock()

	return &MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"resources": resources},
	}
}

// handleResourcesRead reads a specific resource by URI.
func (s *Server) handleResourcesRead(ctx context.Context, req *MCPRequest) *MCPResponse {
	var readParams struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &readParams); err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: errCodeInvalidParams, Message: "invalid resource read params: " + err.Error()},
		}
	}

	// Extract resource name from URI (n42://resourceName).
	name := readParams.URI
	const prefix = "n42://"
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		name = name[len(prefix):]
	}

	s.mu.RLock()
	res, ok := s.resources[name]
	s.mu.RUnlock()

	if !ok {
		return &MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: errCodeResourceNotFound, Message: "resource not found: " + name},
		}
	}

	result, err := res.Handler(ctx)
	if err != nil {
		return &MCPResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &MCPError{Code: errCodeInternal, Message: err.Error()},
		}
	}

	contents := []map[string]interface{}{
		{
			"uri":      readParams.URI,
			"mimeType": "application/json",
			"text":     mustMarshalString(result),
		},
	}

	return &MCPResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  map[string]interface{}{"contents": contents},
	}
}

// writeJSON writes a JSON response to the HTTP response writer.
func writeJSON(w http.ResponseWriter, resp *MCPResponse) {
	w.Header().Set("Content-Type", "application/json")
	if resp.Error != nil {
		w.WriteHeader(http.StatusOK) // JSON-RPC errors still use 200
	}
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Error("MCP: failed to write response", "err", err)
	}
}

// writeError writes a JSON-RPC error response.
func writeError(w http.ResponseWriter, id interface{}, code int, msg string) {
	resp := &MCPResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &MCPError{Code: code, Message: msg},
	}
	writeJSON(w, resp)
}

// mustMarshalString marshals v to a JSON string, returning "{}" on error.
func mustMarshalString(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// hashFromHex parses a hex-encoded hash string. Returns an error if the
// input is not a valid 32-byte hex hash.
func hashFromHex(s string) (types.Hash, error) {
	if len(s) == 0 {
		return types.Hash{}, fmt.Errorf("empty hash")
	}
	h := types.HexToHash(s)
	return h, nil
}

// addressFromHex parses a hex-encoded address string.
func addressFromHex(s string) (types.Address, error) {
	if len(s) == 0 {
		return types.Address{}, fmt.Errorf("empty address")
	}
	return types.HexToAddress(s), nil
}
