package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type stubAgentProvider struct {
	findAgentsResult interface{}
	requestTaskID    string
	requestTaskErr   error
	taskStatus       interface{}
	taskStatusErr    error
	reputation       interface{}
}

func (p *stubAgentProvider) FindAgents(capability string, minReputation float64) interface{} {
	return p.findAgentsResult
}

func (p *stubAgentProvider) RequestTask(requesterDID, capability, inputCAS, maxBudget string) (string, error) {
	return p.requestTaskID, p.requestTaskErr
}

func (p *stubAgentProvider) CheckTaskStatus(negotiationID string) (interface{}, error) {
	return p.taskStatus, p.taskStatusErr
}

func (p *stubAgentProvider) GetReputation(did string) interface{} {
	return p.reputation
}

func TestRegisterAgentToolsFindAgents(t *testing.T) {
	s := newTestServer()
	provider := &stubAgentProvider{findAgentsResult: []string{"agent1"}}
	s.registerAgentTools(provider)

	tool, ok := s.tools["findAgents"]
	if !ok {
		t.Fatal("expected findAgents tool registered")
	}

	res, err := tool.Handler(context.Background(), json.RawMessage(`{"capability":"inference","minReputation":50}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if list, ok := res.([]string); !ok || len(list) != 1 {
		t.Errorf("got %v", res)
	}

	// missing capability
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing capability")
	}

	// invalid params
	if _, err := tool.Handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid params")
	}

	// reputation clamping high
	res2, err2 := tool.Handler(context.Background(), json.RawMessage(`{"capability":"x","minReputation":500}`))
	if err2 != nil || res2 == nil {
		t.Fatalf("unexpected err %v", err2)
	}

	// reputation clamping low (negative)
	res3, err3 := tool.Handler(context.Background(), json.RawMessage(`{"capability":"x","minReputation":-5}`))
	if err3 != nil || res3 == nil {
		t.Fatalf("unexpected err %v", err3)
	}
}

func TestRegisterAgentToolsRequestTask(t *testing.T) {
	s := newTestServer()
	provider := &stubAgentProvider{requestTaskID: "neg123"}
	s.registerAgentTools(provider)

	tool := s.tools["requestAgentTask"]
	params := `{"requesterDID":"did:n42:1","capability":"inference","inputCAS":"0xabc","maxBudget":"1000"}`
	res, err := tool.Handler(context.Background(), json.RawMessage(params))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]string)
	if m["negotiationID"] != "neg123" || m["status"] != "open" {
		t.Errorf("got %v", m)
	}

	// missing fields
	cases := []string{
		`{}`,
		`{"requesterDID":"d"}`,
		`{"requesterDID":"d","capability":"c"}`,
		`{"requesterDID":"d","capability":"c","inputCAS":"i"}`,
	}
	for _, c := range cases {
		if _, err := tool.Handler(context.Background(), json.RawMessage(c)); err == nil {
			t.Errorf("expected error for params %s", c)
		}
	}

	// provider error
	providerErr := &stubAgentProvider{requestTaskErr: errors.New("boom")}
	s2 := newTestServer()
	s2.registerAgentTools(providerErr)
	tool2 := s2.tools["requestAgentTask"]
	if _, err := tool2.Handler(context.Background(), json.RawMessage(params)); err == nil {
		t.Error("expected error from provider")
	}
}

func TestRegisterAgentToolsCheckTaskStatus(t *testing.T) {
	s := newTestServer()
	provider := &stubAgentProvider{taskStatus: map[string]string{"status": "open"}}
	s.registerAgentTools(provider)

	tool := s.tools["checkTaskStatus"]
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"negotiationID":"neg1"}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if res == nil {
		t.Error("expected result")
	}

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing negotiationID")
	}

	s2 := newTestServer()
	s2.registerAgentTools(&stubAgentProvider{taskStatusErr: errors.New("not found")})
	tool2 := s2.tools["checkTaskStatus"]
	if _, err := tool2.Handler(context.Background(), json.RawMessage(`{"negotiationID":"x"}`)); err == nil {
		t.Error("expected error from provider")
	}
}

func TestRegisterAgentToolsGetReputation(t *testing.T) {
	s := newTestServer()
	provider := &stubAgentProvider{reputation: map[string]float64{"score": 0.9}}
	s.registerAgentTools(provider)

	tool := s.tools["getAgentReputation"]
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"did":"did:n42:1"}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if res == nil {
		t.Error("expected result")
	}

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing did")
	}
}
