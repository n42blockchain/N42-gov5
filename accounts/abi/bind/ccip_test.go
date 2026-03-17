package bind

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/holiman/uint256"

	N42 "github.com/n42blockchain/N42"
	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

const ccipTestABI = `[
  {"type":"error","name":"OffchainLookup","inputs":[
    {"name":"sender","type":"address"},
    {"name":"urls","type":"string[]"},
    {"name":"callData","type":"bytes"},
    {"name":"callbackFunction","type":"bytes4"},
    {"name":"extraData","type":"bytes"}]},
  {"type":"function","name":"value","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint256"}]},
  {"type":"function","name":"resolve","stateMutability":"view","inputs":[{"name":"response","type":"bytes"},{"name":"extraData","type":"bytes"}],"outputs":[{"name":"","type":"uint256"}]}
]`

type testCCIPCaller struct {
	t            *testing.T
	contract     types.Address
	firstInput   []byte
	secondInput  []byte
	firstErr     error
	secondOutput []byte
	callCount    int
}

func (m *testCCIPCaller) CodeAt(context.Context, types.Address, *uint256.Int) ([]byte, error) {
	return []byte{0x60, 0x00}, nil
}

func (m *testCCIPCaller) CallContract(_ context.Context, call N42.CallMsg, _ *uint256.Int) ([]byte, error) {
	m.callCount++
	switch m.callCount {
	case 1:
		if !bytes.Equal(call.Data, m.firstInput) {
			m.t.Fatalf("first call data = %x, want %x", call.Data, m.firstInput)
		}
		return nil, m.firstErr
	case 2:
		if !bytes.Equal(call.Data, m.secondInput) {
			m.t.Fatalf("second call data = %x, want %x", call.Data, m.secondInput)
		}
		return m.secondOutput, nil
	default:
		m.t.Fatalf("unexpected call #%d", m.callCount)
		return nil, nil
	}
}

type revertRPCError struct {
	message string
	data    string
}

func (e *revertRPCError) Error() string          { return e.message }
func (e *revertRPCError) ErrorCode() int         { return 3 }
func (e *revertRPCError) ErrorData() interface{} { return e.data }

func TestCallFollowsOffchainLookupGET(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(ccipTestABI))
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	contract := types.HexToAddress("0x1111111111111111111111111111111111111111")
	initialInput, err := parsed.Pack("value")
	if err != nil {
		t.Fatalf("Pack(value) error = %v", err)
	}

	var callbackID [4]byte
	copy(callbackID[:], parsed.Methods["resolve"].ID)
	extraData := []byte{0xaa, 0xbb, 0xcc}
	response := []byte{0x12, 0x34, 0x56}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("gateway method = %s, want GET", r.Method)
		}
		if got, want := r.URL.Path, "/"+strings.ToLower(contract.Hex())+"/"+hexutil.Encode([]byte{0xde, 0xad, 0xbe, 0xef}); got != want {
			t.Fatalf("gateway path = %s, want %s", got, want)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"data": hexutil.Encode(response),
		})
	}))
	defer server.Close()

	revertData := mustPackOffchainLookup(t, parsed, contract, []string{server.URL + "/{sender}/{data}"}, []byte{0xde, 0xad, 0xbe, 0xef}, callbackID, extraData)
	callbackArgs, err := abi.PackOffchainLookupResponse(response, extraData)
	if err != nil {
		t.Fatalf("PackOffchainLookupResponse() error = %v", err)
	}
	secondInput := append(append([]byte{}, callbackID[:]...), callbackArgs...)
	output, err := parsed.Methods["resolve"].Outputs.Pack(big.NewInt(42))
	if err != nil {
		t.Fatalf("Pack(resolve output) error = %v", err)
	}

	caller := &testCCIPCaller{
		t:           t,
		contract:    contract,
		firstInput:  initialInput,
		secondInput: secondInput,
		firstErr: &revertRPCError{
			message: "execution reverted",
			data:    hexutil.Encode(revertData),
		},
		secondOutput: output,
	}
	bound := NewBoundContract(contract, parsed, caller, nil, nil)

	var results []interface{}
	if err := bound.Call(&CallOpts{
		Context:        context.Background(),
		EnableCCIPRead: true,
		CCIPReadClient: server.Client(),
	}, &results, "value"); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if caller.callCount != 2 {
		t.Fatalf("callCount = %d, want 2", caller.callCount)
	}
	got, ok := results[0].(*big.Int)
	if !ok || got.Cmp(big.NewInt(42)) != 0 {
		t.Fatalf("results = %#v", results)
	}
}

func TestCallFollowsOffchainLookupPOST(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(ccipTestABI))
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	contract := types.HexToAddress("0x2222222222222222222222222222222222222222")
	initialInput, err := parsed.Pack("value")
	if err != nil {
		t.Fatalf("Pack(value) error = %v", err)
	}

	var callbackID [4]byte
	copy(callbackID[:], parsed.Methods["resolve"].ID)
	extraData := []byte{0x99}
	response := []byte{0xfe, 0xed}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("gateway method = %s, want POST", r.Method)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if payload["sender"] != strings.ToLower(contract.Hex()) {
			t.Fatalf("sender = %s", payload["sender"])
		}
		if payload["data"] != hexutil.Encode([]byte{0xca, 0xfe}) {
			t.Fatalf("data = %s", payload["data"])
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"data": hexutil.Encode(response),
		})
	}))
	defer server.Close()

	revertData := mustPackOffchainLookup(t, parsed, contract, []string{server.URL}, []byte{0xca, 0xfe}, callbackID, extraData)
	callbackArgs, err := abi.PackOffchainLookupResponse(response, extraData)
	if err != nil {
		t.Fatalf("PackOffchainLookupResponse() error = %v", err)
	}
	secondInput := append(append([]byte{}, callbackID[:]...), callbackArgs...)
	output, err := parsed.Methods["resolve"].Outputs.Pack(big.NewInt(7))
	if err != nil {
		t.Fatalf("Pack(resolve output) error = %v", err)
	}

	caller := &testCCIPCaller{
		t:           t,
		contract:    contract,
		firstInput:  initialInput,
		secondInput: secondInput,
		firstErr: &revertRPCError{
			message: "execution reverted",
			data:    hexutil.Encode(revertData),
		},
		secondOutput: output,
	}
	bound := NewBoundContract(contract, parsed, caller, nil, nil)

	var results []interface{}
	if err := bound.Call(&CallOpts{
		Context:        context.Background(),
		EnableCCIPRead: true,
		CCIPReadClient: server.Client(),
	}, &results, "value"); err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	got, ok := results[0].(*big.Int)
	if !ok || got.Cmp(big.NewInt(7)) != 0 {
		t.Fatalf("results = %#v", results)
	}
}

func TestCallRejectsOffchainLookupSenderMismatch(t *testing.T) {
	parsed, err := abi.JSON(strings.NewReader(ccipTestABI))
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	contract := types.HexToAddress("0x3333333333333333333333333333333333333333")
	initialInput, err := parsed.Pack("value")
	if err != nil {
		t.Fatalf("Pack(value) error = %v", err)
	}
	var callbackID [4]byte
	copy(callbackID[:], parsed.Methods["resolve"].ID)

	revertData := mustPackOffchainLookup(
		t,
		parsed,
		types.HexToAddress("0x4444444444444444444444444444444444444444"),
		[]string{"https://example.invalid/{sender}/{data}"},
		[]byte{0xde},
		callbackID,
		[]byte{0xad},
	)
	caller := &testCCIPCaller{
		t:          t,
		contract:   contract,
		firstInput: initialInput,
		firstErr: &revertRPCError{
			message: "execution reverted",
			data:    hexutil.Encode(revertData),
		},
	}
	bound := NewBoundContract(contract, parsed, caller, nil, nil)

	var results []interface{}
	err = bound.Call(&CallOpts{
		Context:        context.Background(),
		EnableCCIPRead: true,
	}, &results, "value")
	if err == nil || !strings.Contains(err.Error(), "sender mismatch") {
		t.Fatalf("Call() error = %v, want sender mismatch", err)
	}
}

func mustPackOffchainLookup(t *testing.T, parsed abi.ABI, sender types.Address, urls []string, callData []byte, callbackID [4]byte, extraData []byte) []byte {
	t.Helper()
	payload, err := parsed.Errors["OffchainLookup"].Inputs.Pack(sender, urls, callData, callbackID, extraData)
	if err != nil {
		t.Fatalf("Pack(OffchainLookup) error = %v", err)
	}
	lookupErr := parsed.Errors["OffchainLookup"]
	return append(append([]byte{}, lookupErr.ID[:4]...), payload...)
}
