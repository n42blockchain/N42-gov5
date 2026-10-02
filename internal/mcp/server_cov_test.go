package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestServer() *Server {
	return &Server{
		tools:     make(map[string]Tool),
		resources: make(map[string]ResourceProvider),
		stopped:   make(chan struct{}),
	}
}

func TestRegisterToolAllowlist(t *testing.T) {
	s := newTestServer()
	s.allowedTools = map[string]struct{}{"allowed": {}}

	s.RegisterTool(Tool{Name: "allowed"})
	s.RegisterTool(Tool{Name: "denied"})

	if _, ok := s.tools["allowed"]; !ok {
		t.Error("expected allowed tool registered")
	}
	if _, ok := s.tools["denied"]; ok {
		t.Error("expected denied tool to be skipped")
	}
}

func TestRegisterResource(t *testing.T) {
	s := newTestServer()
	s.RegisterResource(ResourceProvider{Name: "res1"})
	if _, ok := s.resources["res1"]; !ok {
		t.Error("expected resource registered")
	}
}

func TestHandleInitialize(t *testing.T) {
	s := newTestServer()
	s.RegisterTool(Tool{Name: "t1"})
	s.RegisterResource(ResourceProvider{Name: "r1"})

	resp := s.handleInitialize(&MCPRequest{ID: 1})
	if resp.Error != nil {
		t.Fatalf("unexpected error %+v", resp.Error)
	}
	m := resp.Result.(map[string]interface{})
	if m["protocolVersion"] != protocolVersion {
		t.Errorf("protocolVersion = %v", m["protocolVersion"])
	}
}

func TestHandleToolsList(t *testing.T) {
	s := newTestServer()
	s.RegisterTool(Tool{Name: "t1", Description: "desc", Parameters: json.RawMessage(`{"type":"object"}`)})

	resp := s.handleToolsList(&MCPRequest{ID: 2})
	m := resp.Result.(map[string]interface{})
	tools := m["tools"].([]map[string]interface{})
	if len(tools) != 1 || tools[0]["name"] != "t1" {
		t.Errorf("got %v", tools)
	}
}

func TestHandleToolsCall(t *testing.T) {
	s := newTestServer()
	s.RegisterTool(Tool{
		Name: "echo",
		Handler: func(_ context.Context, params json.RawMessage) (interface{}, error) {
			return map[string]string{"echo": string(params)}, nil
		},
	})

	req := &MCPRequest{ID: 3, Params: json.RawMessage(`{"name":"echo","arguments":{"x":1}}`)}
	resp := s.handleToolsCall(context.Background(), req)
	if resp.Error != nil {
		t.Fatalf("unexpected error %+v", resp.Error)
	}

	// unknown tool
	req2 := &MCPRequest{ID: 4, Params: json.RawMessage(`{"name":"missing"}`)}
	resp2 := s.handleToolsCall(context.Background(), req2)
	if resp2.Error == nil || resp2.Error.Code != errCodeToolNotFound {
		t.Errorf("got %+v", resp2.Error)
	}

	// invalid params
	req3 := &MCPRequest{ID: 5, Params: json.RawMessage(`not json`)}
	resp3 := s.handleToolsCall(context.Background(), req3)
	if resp3.Error == nil || resp3.Error.Code != errCodeInvalidParams {
		t.Errorf("got %+v", resp3.Error)
	}

	// handler returns error
	s.RegisterTool(Tool{
		Name: "fails",
		Handler: func(_ context.Context, _ json.RawMessage) (interface{}, error) {
			return nil, context.DeadlineExceeded
		},
	})
	req4 := &MCPRequest{ID: 6, Params: json.RawMessage(`{"name":"fails"}`)}
	resp4 := s.handleToolsCall(context.Background(), req4)
	if resp4.Error == nil || resp4.Error.Code != errCodeInternal {
		t.Errorf("got %+v", resp4.Error)
	}
}

func TestHandleResourcesListAndRead(t *testing.T) {
	s := newTestServer()
	s.RegisterResource(ResourceProvider{
		Name:        "chainId",
		Description: "chain id",
		Handler: func(_ context.Context) (interface{}, error) {
			return 1, nil
		},
	})

	listResp := s.handleResourcesList(&MCPRequest{ID: 1})
	m := listResp.Result.(map[string]interface{})
	resources := m["resources"].([]map[string]interface{})
	if len(resources) != 1 || resources[0]["name"] != "chainId" {
		t.Errorf("got %v", resources)
	}

	readReq := &MCPRequest{ID: 2, Params: json.RawMessage(`{"uri":"n42://chainId"}`)}
	readResp := s.handleResourcesRead(context.Background(), readReq)
	if readResp.Error != nil {
		t.Fatalf("unexpected error %+v", readResp.Error)
	}

	// not found
	readReq2 := &MCPRequest{ID: 3, Params: json.RawMessage(`{"uri":"n42://missing"}`)}
	readResp2 := s.handleResourcesRead(context.Background(), readReq2)
	if readResp2.Error == nil || readResp2.Error.Code != errCodeResourceNotFound {
		t.Errorf("got %+v", readResp2.Error)
	}

	// invalid params
	readReq3 := &MCPRequest{ID: 4, Params: json.RawMessage(`not json`)}
	readResp3 := s.handleResourcesRead(context.Background(), readReq3)
	if readResp3.Error == nil || readResp3.Error.Code != errCodeInvalidParams {
		t.Errorf("got %+v", readResp3.Error)
	}

	// resource handler error
	s.RegisterResource(ResourceProvider{
		Name: "broken",
		Handler: func(_ context.Context) (interface{}, error) {
			return nil, context.DeadlineExceeded
		},
	})
	readReq4 := &MCPRequest{ID: 5, Params: json.RawMessage(`{"uri":"n42://broken"}`)}
	readResp4 := s.handleResourcesRead(context.Background(), readReq4)
	if readResp4.Error == nil || readResp4.Error.Code != errCodeInternal {
		t.Errorf("got %+v", readResp4.Error)
	}
}

func TestHandleRequestDispatch(t *testing.T) {
	s := newTestServer()
	resp := s.handleRequest(context.Background(), &MCPRequest{ID: 1, Method: "unknown_method"})
	if resp.Error == nil || resp.Error.Code != errCodeMethodNotFound {
		t.Errorf("got %+v", resp.Error)
	}

	resp2 := s.handleRequest(context.Background(), &MCPRequest{ID: 2, Method: "initialize"})
	if resp2.Error != nil {
		t.Errorf("unexpected error %+v", resp2.Error)
	}
}

func TestServeHTTP(t *testing.T) {
	s := newTestServer()

	// non-POST request
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var resp MCPResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error == nil || resp.Error.Code != errCodeInvalidRequest {
		t.Errorf("got %+v", resp.Error)
	}

	// wrong content type
	req2 := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`))
	req2.Header.Set("Content-Type", "text/plain")
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req2)
	var resp2 MCPResponse
	json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if resp2.Error == nil || resp2.Error.Code != errCodeInvalidRequest {
		t.Errorf("got %+v", resp2.Error)
	}

	// invalid JSON body
	req3 := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`not json`))
	req3.Header.Set("Content-Type", "application/json")
	rec3 := httptest.NewRecorder()
	s.ServeHTTP(rec3, req3)
	var resp3 MCPResponse
	json.Unmarshal(rec3.Body.Bytes(), &resp3)
	if resp3.Error == nil || resp3.Error.Code != errCodeParse {
		t.Errorf("got %+v", resp3.Error)
	}

	// wrong jsonrpc version
	req4 := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"jsonrpc":"1.0","id":1,"method":"initialize"}`))
	req4.Header.Set("Content-Type", "application/json")
	rec4 := httptest.NewRecorder()
	s.ServeHTTP(rec4, req4)
	var resp4 MCPResponse
	json.Unmarshal(rec4.Body.Bytes(), &resp4)
	if resp4.Error == nil || resp4.Error.Code != errCodeInvalidRequest {
		t.Errorf("got %+v", resp4.Error)
	}

	// valid initialize request
	req5 := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`))
	req5.Header.Set("Content-Type", "application/json")
	rec5 := httptest.NewRecorder()
	s.ServeHTTP(rec5, req5)
	var resp5 MCPResponse
	json.Unmarshal(rec5.Body.Bytes(), &resp5)
	if resp5.Error != nil {
		t.Errorf("unexpected error %+v", resp5.Error)
	}
}

func TestStopWithoutStart(t *testing.T) {
	s := newTestServer()
	if err := s.Stop(); err != nil {
		t.Errorf("Stop() on unstarted server should be nil, got %v", err)
	}
}
