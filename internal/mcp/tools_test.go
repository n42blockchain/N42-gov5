package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
)

type mcpBackendStub struct {
	chain common.IBlockChain
}

func (b *mcpBackendStub) BlockChain() common.IBlockChain { return b.chain }
func (b *mcpBackendStub) Database() kv.RwDB              { return nil }
func (b *mcpBackendStub) TxPool() common.ITxsPool        { return nil }
func (b *mcpBackendStub) SyncProgress() *SyncStatus      { return nil }
func (b *mcpBackendStub) PeerCount() int                 { return 0 }

type mcpChainStub struct {
	common.IBlockChain
	current block.IBlock
	blocks  map[uint64]block.IBlock
}

func (c *mcpChainStub) CurrentBlock() block.IBlock {
	return c.current
}

func (c *mcpChainStub) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	if number == nil {
		return nil, nil
	}
	return c.blocks[number.Uint64()], nil
}

func TestToolGetTransactionIncludesGenesisInDefaultScanWindow(t *testing.T) {
	targetTx := newTestTransaction(0)
	chain := &mcpChainStub{
		current: newTestBlock(128, nil),
		blocks: map[uint64]block.IBlock{
			0:   newTestBlock(0, []*transaction.Transaction{targetTx}),
			128: newTestBlock(128, nil),
		},
	}
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	params, err := json.Marshal(getTransactionParams{Hash: targetTx.Hash().Hex()})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := server.toolGetTransaction(context.Background(), params)
	if err != nil {
		t.Fatalf("toolGetTransaction() error = %v", err)
	}

	txResult, ok := result.(*txResult)
	if !ok {
		t.Fatalf("toolGetTransaction() type = %T", result)
	}
	if txResult.Block != 0 {
		t.Fatalf("toolGetTransaction() block = %d, want 0", txResult.Block)
	}
	if txResult.Hash != targetTx.Hash().Hex() {
		t.Fatalf("toolGetTransaction() hash = %s, want %s", txResult.Hash, targetTx.Hash().Hex())
	}
}

func TestToolGetTransactionIncludesLowerBoundOfRecentScanWindow(t *testing.T) {
	targetTx := newTestTransaction(1)
	chain := &mcpChainStub{
		current: newTestBlock(129, nil),
		blocks: map[uint64]block.IBlock{
			1:   newTestBlock(1, []*transaction.Transaction{targetTx}),
			129: newTestBlock(129, nil),
		},
	}
	server := &Server{backend: &mcpBackendStub{chain: chain}}

	params, err := json.Marshal(getTransactionParams{Hash: targetTx.Hash().Hex()})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	result, err := server.toolGetTransaction(context.Background(), params)
	if err != nil {
		t.Fatalf("toolGetTransaction() error = %v", err)
	}

	txResult, ok := result.(*txResult)
	if !ok {
		t.Fatalf("toolGetTransaction() type = %T", result)
	}
	if txResult.Block != 1 {
		t.Fatalf("toolGetTransaction() block = %d, want 1", txResult.Block)
	}
}

func newTestBlock(number uint64, txs []*transaction.Transaction) block.IBlock {
	return block.NewBlock(&block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, txs)
}

func newTestTransaction(nonce uint64) *transaction.Transaction {
	from := types.HexToAddress("0x1000000000000000000000000000000000000001")
	to := types.HexToAddress("0x2000000000000000000000000000000000000002")
	return transaction.NewTransaction(
		nonce,
		from,
		&to,
		uint256.NewInt(1),
		21000,
		uint256.NewInt(1),
		nil,
	)
}
