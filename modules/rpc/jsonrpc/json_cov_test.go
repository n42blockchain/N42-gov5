package jsonrpc

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestJsonrpcMessagePredicates(t *testing.T) {
	notif := &jsonrpcMessage{Method: "eth_subscription"}
	if !notif.isNotification() {
		t.Error("expected notification")
	}
	if notif.isCall() {
		t.Error("notification should not be a call")
	}

	call := &jsonrpcMessage{ID: json.RawMessage("1"), Method: "eth_blockNumber"}
	if !call.isCall() {
		t.Error("expected call")
	}
	if call.isNotification() {
		t.Error("call should not be notification")
	}

	resp := &jsonrpcMessage{ID: json.RawMessage("1"), Result: json.RawMessage("1")}
	if !resp.isResponse() {
		t.Error("expected response")
	}

	bad := &jsonrpcMessage{ID: json.RawMessage("{\"x\":1}"), Method: "x"}
	if bad.hasValidID() {
		t.Error("object id should be invalid")
	}

	arrID := &jsonrpcMessage{ID: json.RawMessage("[1]")}
	if arrID.hasValidID() {
		t.Error("array id should be invalid")
	}

	sub := &jsonrpcMessage{Method: "eth_subscribe"}
	if !sub.isSubscribe() {
		t.Error("expected isSubscribe true")
	}
	unsub := &jsonrpcMessage{Method: "eth_unsubscribe"}
	if !unsub.isUnsubscribe() {
		t.Error("expected isUnsubscribe true")
	}

	ns := &jsonrpcMessage{Method: "eth_getBalance"}
	if ns.namespace() != "eth" {
		t.Errorf("namespace() = %q want eth", ns.namespace())
	}
}

func TestJsonrpcMessageStringAndResponse(t *testing.T) {
	msg := &jsonrpcMessage{Version: vsn, ID: json.RawMessage("1"), Method: "eth_foo"}
	s := msg.String()
	if s == "" {
		t.Error("String() returned empty")
	}

	resp := msg.response("hello")
	if resp.Version != vsn {
		t.Errorf("version = %q", resp.Version)
	}
	var out string
	if err := json.Unmarshal(resp.Result, &out); err != nil || out != "hello" {
		t.Errorf("unexpected result: %s err %v", resp.Result, err)
	}

	errResp := msg.errorResponse(errors.New("boom"))
	if errResp.Error == nil || errResp.Error.Message != "boom" {
		t.Errorf("unexpected error response: %+v", errResp.Error)
	}
	if string(errResp.ID) != string(msg.ID) {
		t.Errorf("errorResponse should carry original ID")
	}
}

type codedErr struct{}

func (codedErr) Error() string { return "coded" }
func (codedErr) ErrorCode() int { return 123 }

type dataErr struct{}

func (dataErr) Error() string         { return "withdata" }
func (dataErr) ErrorData() interface{} { return "extra" }

func TestErrorMessage(t *testing.T) {
	m := errorMessage(errors.New("plain"))
	if m.Error.Code != defaultErrorCode {
		t.Errorf("code = %d want %d", m.Error.Code, defaultErrorCode)
	}

	m2 := errorMessage(codedErr{})
	if m2.Error.Code != 123 {
		t.Errorf("code = %d want 123", m2.Error.Code)
	}

	m3 := errorMessage(dataErr{})
	if m3.Error.Data != "extra" {
		t.Errorf("data = %v want extra", m3.Error.Data)
	}
}

func TestJsonErrorMethods(t *testing.T) {
	e := &jsonError{Code: 7}
	if e.Error() != "json-rpc error 7" {
		t.Errorf("Error() = %q", e.Error())
	}
	e2 := &jsonError{Code: 8, Message: "custom"}
	if e2.Error() != "custom" {
		t.Errorf("Error() = %q", e2.Error())
	}
	if e2.ErrorCode() != 8 {
		t.Errorf("ErrorCode() = %d", e2.ErrorCode())
	}
	e3 := &jsonError{Data: "d"}
	if e3.ErrorData() != "d" {
		t.Errorf("ErrorData() = %v", e3.ErrorData())
	}
}

func TestIsBatch(t *testing.T) {
	if !isBatch(json.RawMessage("  [1,2]")) {
		t.Error("expected batch true")
	}
	if isBatch(json.RawMessage("  {\"a\":1}")) {
		t.Error("expected batch false")
	}
	if isBatch(json.RawMessage("")) {
		t.Error("expected batch false for empty")
	}
}

func TestParseMessageSingleAndBatch(t *testing.T) {
	msgs, batch, err := parseMessage(json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"eth_foo"}`))
	if err != nil || batch || len(msgs) != 1 {
		t.Fatalf("got %v %v %v", msgs, batch, err)
	}
	if msgs[0].Method != "eth_foo" {
		t.Errorf("method = %q", msgs[0].Method)
	}

	msgs2, batch2, err2 := parseMessage(json.RawMessage(`[{"jsonrpc":"2.0","id":1,"method":"a"},{"jsonrpc":"2.0","id":2,"method":"b"}]`))
	if err2 != nil || !batch2 || len(msgs2) != 2 {
		t.Fatalf("got %v %v %v", msgs2, batch2, err2)
	}

	// invalid single message falls back to empty message, no error
	msgs3, _, err3 := parseMessage(json.RawMessage(`not json`))
	if err3 != nil {
		t.Fatalf("unexpected error %v", err3)
	}
	if msgs3[0].Method != "" {
		t.Errorf("expected empty method on parse failure")
	}

	// batch too large
	big := "["
	for i := 0; i < maxBatchSize+1; i++ {
		if i > 0 {
			big += ","
		}
		big += `{"jsonrpc":"2.0","id":1,"method":"a"}`
	}
	big += "]"
	if _, _, err4 := parseMessage(json.RawMessage(big)); err4 == nil {
		t.Error("expected error for oversized batch")
	}

	// trailing data after batch
	if _, _, err5 := parseMessage(json.RawMessage(`[1,2]extra`)); err5 == nil {
		t.Error("expected error for trailing data")
	}
}

func TestParsePositionalArguments(t *testing.T) {
	types := []reflect.Type{reflect.TypeOf(int(0)), reflect.TypeOf("")}
	args, err := parsePositionalArguments(json.RawMessage(`[1,"x"]`), types)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if len(args) != 2 || args[0].Int() != 1 || args[1].String() != "x" {
		t.Errorf("got %v", args)
	}

	// empty args with no required types
	args2, err2 := parsePositionalArguments(json.RawMessage(``), nil)
	if err2 != nil || len(args2) != 0 {
		t.Errorf("got %v %v", args2, err2)
	}

	// missing required non-pointer argument
	_, err3 := parsePositionalArguments(json.RawMessage(`[]`), types)
	if err3 == nil {
		t.Error("expected error for missing required arg")
	}

	// non-array args
	_, err4 := parsePositionalArguments(json.RawMessage(`{"a":1}`), types)
	if err4 == nil {
		t.Error("expected error for non-array args")
	}

	// too many args
	_, err5 := parsePositionalArguments(json.RawMessage(`[1,"x",2]`), types)
	if err5 == nil {
		t.Error("expected error for too many args")
	}
}

func TestParseSubscriptionName(t *testing.T) {
	name, err := parseSubscriptionName(json.RawMessage(`["newHeads"]`))
	if err != nil || name != "newHeads" {
		t.Errorf("got %q %v", name, err)
	}

	if _, err := parseSubscriptionName(json.RawMessage(`{"a":1}`)); err == nil {
		t.Error("expected error for non-array")
	}

	if _, err := parseSubscriptionName(json.RawMessage(`[1]`)); err == nil {
		t.Error("expected error for non-string first arg")
	}
}
