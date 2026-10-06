package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// chainStubWithLogs extends chainStubWithConfig with a GetLogs implementation
// for toolGetLogs coverage.
type chainStubWithLogs struct {
	*chainStubWithConfig
	logsByHash map[types.Hash][][]*block.Log
}

func (c *chainStubWithLogs) GetLogs(hash types.Hash) ([][]*block.Log, error) {
	return c.logsByHash[hash], nil
}

func TestToolGetLogs(t *testing.T) {
	blk := newTestBlock(1, nil)
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	logEntry := &block.Log{Address: addr, Topics: []types.Hash{}}

	base := newChainStub(blk, map[uint64]block.IBlock{1: blk})
	chain := &chainStubWithLogs{
		chainStubWithConfig: base,
		logsByHash: map[types.Hash][][]*block.Log{
			blk.Hash(): {{logEntry}},
		},
	}
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	p, _ := json.Marshal(getLogsParams{FromBlock: uint64Ptr(1), ToBlock: uint64Ptr(1)})
	res, err := server.toolGetLogs(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["count"] != 1 {
		t.Errorf("count = %v", m["count"])
	}

	// invalid params
	if _, err := server.toolGetLogs(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid params")
	}

	// fromBlock > toBlock
	p2, _ := json.Marshal(getLogsParams{FromBlock: uint64Ptr(5), ToBlock: uint64Ptr(1)})
	if _, err := server.toolGetLogs(context.Background(), p2); err == nil {
		t.Error("expected error for fromBlock > toBlock")
	}

	// no current block
	emptyChain := newChainStub(nil, nil)
	server2 := &Server{backend: &mcpBackendStub{chain: emptyChain}}
	if _, err := server2.toolGetLogs(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for missing current block")
	}

	// address + topic filters
	addrStr := addr.Hex()
	p3, _ := json.Marshal(getLogsParams{
		FromBlock: uint64Ptr(1), ToBlock: uint64Ptr(1),
		Address: &addrStr, Topics: []string{"0x" + "00000000000000000000000000000000000000000000000000000000000001"},
	})
	res3, err3 := server.toolGetLogs(context.Background(), p3)
	if err3 != nil {
		t.Fatalf("unexpected err %v", err3)
	}
	m3 := res3.(map[string]interface{})
	if m3["count"] != 0 {
		t.Errorf("expected no match due to topic filter, got %v", m3["count"])
	}
}

func TestToolSearchTransactions(t *testing.T) {
	tx := newTestTransaction(0)
	blk := newTestBlockWithTxs(50, []*transaction.Transaction{tx})
	chain := newChainStub(blk, map[uint64]block.IBlock{50: blk})
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	fromAddr := "0x1000000000000000000000000000000000000001"
	p, _ := json.Marshal(searchTransactionsParams{Address: fromAddr})
	res, err := server.toolSearchTransactions(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	m := res.(map[string]interface{})
	if m["count"] != 1 {
		t.Errorf("count = %v want 1", m["count"])
	}

	// invalid params
	if _, err := server.toolSearchTransactions(context.Background(), json.RawMessage(`not json`)); err == nil {
		t.Error("expected error for invalid params")
	}

	// no current block
	emptyChain := newChainStub(nil, nil)
	server2 := &Server{backend: &mcpBackendStub{chain: emptyChain}}
	p2, _ := json.Marshal(searchTransactionsParams{Address: fromAddr})
	if _, err := server2.toolSearchTransactions(context.Background(), p2); err == nil {
		t.Error("expected error for missing current block")
	}

	// fromBlock > toBlock
	p3, _ := json.Marshal(searchTransactionsParams{Address: fromAddr, FromBlock: uint64Ptr(10), ToBlock: uint64Ptr(1)})
	if _, err := server.toolSearchTransactions(context.Background(), p3); err == nil {
		t.Error("expected error for fromBlock > toBlock")
	}

	// limit clamping
	p4, _ := json.Marshal(searchTransactionsParams{Address: fromAddr, Limit: intPtr(-1)})
	if _, err := server.toolSearchTransactions(context.Background(), p4); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	p5, _ := json.Marshal(searchTransactionsParams{Address: fromAddr, Limit: intPtr(5000)})
	if _, err := server.toolSearchTransactions(context.Background(), p5); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
}

func TestRegisterDefaultTools(t *testing.T) {
	s := newTestServer()
	s.registerDefaultTools()
	for _, name := range []string{
		"getBlock", "getTransaction", "getBalance", "getLogs",
		"getCode", "getStorageAt", "chainInfo", "searchTransactions",
	} {
		if _, ok := s.tools[name]; !ok {
			t.Errorf("expected tool %q registered", name)
		}
	}
}

func TestTxSummaryFromTx(t *testing.T) {
	tx := newTestTransaction(3)
	summary := txSummaryFromTx(tx)
	if summary.Hash != tx.Hash().Hex() {
		t.Errorf("hash mismatch")
	}
	if summary.Nonce != 3 {
		t.Errorf("nonce = %d want 3", summary.Nonce)
	}
	if summary.From == "" {
		t.Error("expected From set")
	}
	if summary.To == "" {
		t.Error("expected To set")
	}
}

func TestMarshalBlockFull(t *testing.T) {
	tx := newTestTransaction(1)
	blk := newTestBlockWithTxs(7, []*transaction.Transaction{tx})

	full := marshalBlock(blk, true)
	if full.TxCount != 1 {
		t.Errorf("txCount = %d", full.TxCount)
	}
	if _, ok := full.Transactions.([]txSummary); !ok {
		t.Errorf("expected full tx summaries, got %T", full.Transactions)
	}

	summaryOnly := marshalBlock(blk, false)
	if _, ok := summaryOnly.Transactions.([]string); !ok {
		t.Errorf("expected tx hashes, got %T", summaryOnly.Transactions)
	}
}

func newTestBlockWithTxs(number uint64, txs []*transaction.Transaction) block.IBlock {
	return newTestBlock(number, txs)
}
