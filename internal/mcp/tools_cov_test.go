package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// chainStubWithConfig extends mcpChainStub with Config/GetBlockByHash support
// for tests that exercise toolGetBlock and toolChainInfo.
type chainStubWithConfig struct {
	*mcpChainStub
	cfg        *params.ChainConfig
	byHash     map[types.Hash]block.IBlock
	byHashErr  error
}

func (c *chainStubWithConfig) Config() *params.ChainConfig {
	return c.cfg
}

func (c *chainStubWithConfig) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	if c.byHashErr != nil {
		return nil, c.byHashErr
	}
	return c.byHash[h], nil
}

func newChainStub(current block.IBlock, blocks map[uint64]block.IBlock) *chainStubWithConfig {
	return &chainStubWithConfig{
		mcpChainStub: &mcpChainStub{current: current, blocks: blocks},
		byHash:       map[types.Hash]block.IBlock{},
	}
}

func TestToolGetBlockByNumberAndLatest(t *testing.T) {
	blk0 := newTestBlock(0, nil)
	current := newTestBlock(5, nil)
	chain := newChainStub(current, map[uint64]block.IBlock{0: blk0, 5: current})
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	// by number
	params, _ := json.Marshal(getBlockParams{Number: uint64Ptr(0)})
	res, err := server.toolGetBlock(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	br, ok := res.(*blockResult)
	if !ok || br.Number != 0 {
		t.Fatalf("got %+v", res)
	}

	// latest (no number/hash given)
	res2, err2 := server.toolGetBlock(context.Background(), json.RawMessage(`{}`))
	if err2 != nil {
		t.Fatalf("unexpected err %v", err2)
	}
	br2 := res2.(*blockResult)
	if br2.Number != 5 {
		t.Fatalf("got %d want 5", br2.Number)
	}
}

func TestToolGetBlockByHash(t *testing.T) {
	blk := newTestBlock(3, nil)
	h := blk.Hash()
	chain := newChainStub(blk, map[uint64]block.IBlock{3: blk})
	chain.byHash[h] = blk
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	p, _ := json.Marshal(getBlockParams{Hash: strPtr(h.Hex())})
	res, err := server.toolGetBlock(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	br := res.(*blockResult)
	if br.Number != 3 {
		t.Fatalf("got %d want 3", br.Number)
	}
}

func TestToolGetBlockNotFound(t *testing.T) {
	chain := newChainStub(nil, map[uint64]block.IBlock{})
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	p, _ := json.Marshal(getBlockParams{Number: uint64Ptr(99)})
	if _, err := server.toolGetBlock(context.Background(), p); err == nil {
		t.Error("expected error for missing block")
	}

	// invalid params
	if _, err := server.toolGetBlock(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid params")
	}
}

func TestToolChainInfo(t *testing.T) {
	current := newTestBlock(10, nil)
	chain := newChainStub(current, map[uint64]block.IBlock{10: current})
	chain.cfg = &params.ChainConfig{ChainID: uint256.NewInt(42).ToBig(), ChainName: "test-chain"}
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	res, err := server.toolChainInfo(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["peer_count"] != 0 {
		t.Errorf("peer_count = %v", m["peer_count"])
	}
	if m["chain_id"] != uint64(42) {
		t.Errorf("chain_id = %v", m["chain_id"])
	}
	if m["chain_name"] != "test-chain" {
		t.Errorf("chain_name = %v", m["chain_name"])
	}
	if m["latest_block"] != uint64(10) {
		t.Errorf("latest_block = %v", m["latest_block"])
	}
}

func TestMatchLog(t *testing.T) {
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	other := types.HexToAddress("0x2000000000000000000000000000000000000002")
	topic1 := types.HexToHash("0xaa")

	l := &block.Log{Address: addr, Topics: []types.Hash{topic1}}

	if !matchLog(l, &addr, nil) {
		t.Error("expected match with matching address, no topics")
	}
	if matchLog(l, &other, nil) {
		t.Error("expected no match with different address")
	}
	if !matchLog(l, nil, []types.Hash{topic1}) {
		t.Error("expected topic match")
	}
	if matchLog(l, nil, []types.Hash{types.HexToHash("0xbb")}) {
		t.Error("expected topic mismatch to fail")
	}
	// wildcard topic
	if !matchLog(l, nil, []types.Hash{{}}) {
		t.Error("expected wildcard topic to match")
	}
	// topics longer than log topics
	if matchLog(l, nil, []types.Hash{topic1, types.HexToHash("0xcc")}) {
		t.Error("expected mismatch when filter has more topics than log")
	}
}

func TestMarshalLog(t *testing.T) {
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	topic := types.HexToHash("0xaa")
	l := &block.Log{
		Address:     addr,
		Topics:      []types.Hash{topic},
		Data:        []byte{0x01, 0x02},
		BlockNumber: uint256.NewInt(5),
		TxHash:      types.HexToHash("0xbb"),
		TxIndex:     1,
		Index:       2,
		Removed:     false,
	}
	r := marshalLog(l)
	if r.Address != addr.Hex() {
		t.Errorf("address = %s", r.Address)
	}
	if len(r.Topics) != 1 || r.Topics[0] != topic.Hex() {
		t.Errorf("topics = %v", r.Topics)
	}
	if r.Data != "0x0102" {
		t.Errorf("data = %s", r.Data)
	}
	if r.BlockNumber != 5 {
		t.Errorf("blockNumber = %d", r.BlockNumber)
	}

	// nil BlockNumber
	l2 := &block.Log{Address: addr}
	r2 := marshalLog(l2)
	if r2.BlockNumber != 0 {
		t.Errorf("blockNumber = %d want 0", r2.BlockNumber)
	}
}

func TestAddrStr(t *testing.T) {
	if addrStr(nil) != "" {
		t.Error("expected empty string for nil address")
	}
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	if addrStr(&addr) != addr.Hex() {
		t.Errorf("got %s want %s", addrStr(&addr), addr.Hex())
	}
}

func TestHashFromHexAndAddressFromHex(t *testing.T) {
	if _, err := hashFromHex(""); err == nil {
		t.Error("expected error for empty hash")
	}
	h, err := hashFromHex("0x" + "00000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if h == (types.Hash{}) {
		t.Error("expected non-zero hash")
	}

	if _, err := addressFromHex(""); err == nil {
		t.Error("expected error for empty address")
	}
	a, err := addressFromHex("0x1000000000000000000000000000000000000001")
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if a == (types.Address{}) {
		t.Error("expected non-zero address")
	}
}

func TestMustMarshalString(t *testing.T) {
	s := mustMarshalString(map[string]int{"a": 1})
	if s != `{"a":1}` {
		t.Errorf("got %s", s)
	}
	// unmarshalable value (channel) falls back to "{}"
	s2 := mustMarshalString(make(chan int))
	if s2 != "{}" {
		t.Errorf("got %s want {}", s2)
	}
}

func TestWriteJSONAndWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, &MCPResponse{JSONRPC: "2.0", ID: 1, Result: "ok"})
	if rec.Code != 200 {
		t.Errorf("code = %d", rec.Code)
	}
	var resp MCPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal err %v", err)
	}
	if resp.Result != "ok" {
		t.Errorf("result = %v", resp.Result)
	}

	rec2 := httptest.NewRecorder()
	writeError(rec2, 1, -32600, "bad request")
	var resp2 MCPResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("unmarshal err %v", err)
	}
	if resp2.Error == nil || resp2.Error.Code != -32600 || resp2.Error.Message != "bad request" {
		t.Errorf("got %+v", resp2.Error)
	}
}

func uint64Ptr(v uint64) *uint64 { return &v }
func strPtr(s string) *string    { return &s }
