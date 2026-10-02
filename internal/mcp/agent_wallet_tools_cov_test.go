package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stubWalletProvider struct {
	createAddr string
	createErr  error
	balance    string
	balanceErr error
	count      int
}

func (p *stubWalletProvider) CreateAccount(ownerKey []byte, agentDID string) (string, error) {
	return p.createAddr, p.createErr
}

func (p *stubWalletProvider) GetBalance(address string) (string, error) {
	return p.balance, p.balanceErr
}

func (p *stubWalletProvider) AccountCount() int { return p.count }

func TestRegisterAgentWalletToolsNilProvider(t *testing.T) {
	s := newTestServer()
	s.registerAgentWalletTools(nil)
	if len(s.tools) != 0 {
		t.Errorf("expected no tools registered for nil provider, got %d", len(s.tools))
	}
}

func TestRegisterAgentWalletToolsCreateAccount(t *testing.T) {
	s := newTestServer()
	s.registerAgentWalletTools(&stubWalletProvider{createAddr: "0xabc"})

	tool := s.tools["createAgentWallet"]
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"ownerAddress":"0x1","agentDID":"did:n42:1"}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["address"] != "0xabc" || m["status"] != "created" {
		t.Errorf("got %v", m)
	}

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing fields")
	}
	if _, err := tool.Handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid json")
	}

	s2 := newTestServer()
	s2.registerAgentWalletTools(&stubWalletProvider{createErr: errors.New("fail")})
	tool2 := s2.tools["createAgentWallet"]
	if _, err := tool2.Handler(context.Background(), json.RawMessage(`{"ownerAddress":"0x1","agentDID":"d"}`)); err == nil {
		t.Error("expected error from provider")
	}
}

func TestRegisterAgentWalletToolsGetBalance(t *testing.T) {
	s := newTestServer()
	s.registerAgentWalletTools(&stubWalletProvider{balance: "100"})

	tool := s.tools["getAgentBalance"]
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"address":"0x1"}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["balance"] != "100" {
		t.Errorf("got %v", m)
	}

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing address")
	}

	s2 := newTestServer()
	s2.registerAgentWalletTools(&stubWalletProvider{balanceErr: errors.New("fail")})
	tool2 := s2.tools["getAgentBalance"]
	if _, err := tool2.Handler(context.Background(), json.RawMessage(`{"address":"0x1"}`)); err == nil {
		t.Error("expected error from provider")
	}
}

func TestRegisterAgentWalletToolsSubmitTransactionPlaceholder(t *testing.T) {
	s := newTestServer()
	s.registerAgentWalletTools(&stubWalletProvider{})

	tool := s.tools["submitTransaction"]
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"from":"0x1","to":"0x2"}`)); err == nil {
		t.Error("expected placeholder error")
	}
}
