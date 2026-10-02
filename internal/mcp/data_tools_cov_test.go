package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

type stubIndexProvider struct {
	transfers   interface{}
	profile     interface{}
	events      interface{}
	gasMetrics  interface{}
}

func (p *stubIndexProvider) QueryTransfers(contract, from, to *types.Address, fromBlock, toBlock uint64, limit int) interface{} {
	return p.transfers
}

func (p *stubIndexProvider) GetProfile(addr types.Address) interface{} {
	return p.profile
}

func (p *stubIndexProvider) QueryEvents(contract *types.Address, eventSig *types.Hash, fromBlock, toBlock uint64, limit int) interface{} {
	return p.events
}

func (p *stubIndexProvider) QueryGasMetrics(fromBlock, toBlock uint64) interface{} {
	return p.gasMetrics
}

func TestRegisterDataToolsQueryTokenTransfers(t *testing.T) {
	s := newTestServer()
	provider := &stubIndexProvider{transfers: []string{"t1"}}
	s.registerDataTools(provider)

	tool := s.tools["queryTokenTransfers"]
	addr := "0x1000000000000000000000000000000000000001"
	params, _ := json.Marshal(queryTokenTransfersParams{
		Contract: strPtr(addr), From: strPtr(addr), To: strPtr(addr),
		FromBlock: uint64Ptr(1), ToBlock: uint64Ptr(10), Limit: intPtr(5000),
	})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if res.(map[string]interface{})["transfers"] == nil {
		t.Error("expected transfers result")
	}

	// invalid contract address is accepted by addressFromHex (never errors on
	// non-empty strings), so instead test invalid JSON and limit clamping low.
	if _, err := tool.Handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid json")
	}

	params2, _ := json.Marshal(queryTokenTransfersParams{Limit: intPtr(-1)})
	if _, err := tool.Handler(context.Background(), params2); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
}

func TestRegisterDataToolsGetAddressProfile(t *testing.T) {
	s := newTestServer()
	provider := &stubIndexProvider{profile: map[string]int{"txCount": 5}}
	s.registerDataTools(provider)

	tool := s.tools["getAddressProfile"]
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"address":"0x1000000000000000000000000000000000000001"}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["found"] != true {
		t.Errorf("got %v", m)
	}

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"address":""}`)); err == nil {
		t.Error("expected error for empty address")
	}

	// no profile found
	s2 := newTestServer()
	s2.registerDataTools(&stubIndexProvider{})
	tool2 := s2.tools["getAddressProfile"]
	res2, err2 := tool2.Handler(context.Background(), json.RawMessage(`{"address":"0x1000000000000000000000000000000000000001"}`))
	if err2 != nil {
		t.Fatalf("unexpected err %v", err2)
	}
	m2 := res2.(map[string]interface{})
	if m2["found"] != false {
		t.Errorf("got %v", m2)
	}
}

func TestRegisterDataToolsAnalyzeContract(t *testing.T) {
	s := newTestServer()
	events := []map[string]string{{"EventSig": "0xabc"}, {"EventSig": "0xabc"}, {"EventSig": "0xdef"}}
	provider := &stubIndexProvider{events: events, profile: map[string]int{"x": 1}, transfers: []string{"t"}}
	s.registerDataTools(provider)

	tool := s.tools["analyzeContract"]
	res, err := tool.Handler(context.Background(), json.RawMessage(`{"address":"0x1000000000000000000000000000000000000001"}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["event_count"] != 3 {
		t.Errorf("event_count = %v", m["event_count"])
	}
	sigCounts := m["event_signatures"].(map[string]int)
	if sigCounts["0xabc"] != 2 || sigCounts["0xdef"] != 1 {
		t.Errorf("got %v", sigCounts)
	}

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"address":""}`)); err == nil {
		t.Error("expected error for empty address")
	}
}

func TestRegisterDataToolsGetGasAnalytics(t *testing.T) {
	s := newTestServer()
	metrics := []map[string]interface{}{
		{"GasUsed": 1000, "GasLimit": 2000, "TxCount": 5, "Utilization": 0.5},
		{"GasUsed": 2000, "GasLimit": 2000, "TxCount": 10, "Utilization": 1.0},
	}
	provider := &stubIndexProvider{gasMetrics: metrics}
	s.registerDataTools(provider)

	tool := s.tools["getGasAnalytics"]
	res, err := tool.Handler(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["block_count"] != 2 {
		t.Errorf("block_count = %v", m["block_count"])
	}
	summary := m["summary"].(map[string]interface{})
	if summary["total_tx_count"] != 15 {
		t.Errorf("total_tx_count = %v", summary["total_tx_count"])
	}

	// empty metrics -> avg should stay zero, no divide by zero panic
	s2 := newTestServer()
	s2.registerDataTools(&stubIndexProvider{gasMetrics: []map[string]interface{}{}})
	tool2 := s2.tools["getGasAnalytics"]
	res2, err2 := tool2.Handler(context.Background(), json.RawMessage(`{}`))
	if err2 != nil {
		t.Fatalf("unexpected err %v", err2)
	}
	m2 := res2.(map[string]interface{})
	if m2["block_count"] != 0 {
		t.Errorf("block_count = %v", m2["block_count"])
	}
}

func TestRegisterDataToolsQueryEvents(t *testing.T) {
	s := newTestServer()
	provider := &stubIndexProvider{events: []string{"e1"}}
	s.registerDataTools(provider)

	tool := s.tools["queryEvents"]
	sig := "0x" + "00000000000000000000000000000000000000000000000000000000000001"
	addr := "0x1000000000000000000000000000000000000001"
	params, _ := json.Marshal(queryEventsParams{Address: &addr, EventSignature: &sig, Limit: intPtr(5000)})
	res, err := tool.Handler(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if res.(map[string]interface{})["events"] == nil {
		t.Error("expected events result")
	}

	if _, err := tool.Handler(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid json")
	}
}

func intPtr(v int) *int { return &v }
