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

package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

// registerDefaultTools registers all built-in blockchain query tools.
func (s *Server) registerDefaultTools() {
	s.RegisterTool(Tool{
		Name:        "getBlock",
		Description: "Get block by number or hash. Returns block header, transactions, and metadata.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"number": {"type": "integer", "description": "Block number to retrieve"},
				"hash":   {"type": "string",  "description": "Block hash (hex) to retrieve"},
				"full":   {"type": "boolean", "description": "Include full transaction objects (default: false)"}
			}
		}`),
		Handler: s.toolGetBlock,
	})

	s.RegisterTool(Tool{
		Name:        "getTransaction",
		Description: "Get transaction by hash. Returns transaction details and receipt.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"hash": {"type": "string", "description": "Transaction hash (hex)"}
			},
			"required": ["hash"]
		}`),
		Handler: s.toolGetTransaction,
	})

	s.RegisterTool(Tool{
		Name:        "getBalance",
		Description: "Get ETH balance for an address at latest or specific block.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address": {"type": "string",  "description": "Account address (hex)"},
				"block":   {"type": "integer", "description": "Block number (default: latest)"}
			},
			"required": ["address"]
		}`),
		Handler: s.toolGetBalance,
	})

	s.RegisterTool(Tool{
		Name:        "getLogs",
		Description: "Get event logs matching filter criteria (address, topics, block range).",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address":   {"type": "string",  "description": "Contract address to filter (hex)"},
				"fromBlock": {"type": "integer", "description": "Start block number"},
				"toBlock":   {"type": "integer", "description": "End block number"},
				"topics":    {"type": "array", "items": {"type": "string"}, "description": "Topic filters (hex)"}
			}
		}`),
		Handler: s.toolGetLogs,
	})

	s.RegisterTool(Tool{
		Name:        "getCode",
		Description: "Get contract bytecode at an address.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address": {"type": "string",  "description": "Contract address (hex)"},
				"block":   {"type": "integer", "description": "Block number (default: latest)"}
			},
			"required": ["address"]
		}`),
		Handler: s.toolGetCode,
	})

	s.RegisterTool(Tool{
		Name:        "getStorageAt",
		Description: "Get storage value at a specific slot for a contract address.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address": {"type": "string",  "description": "Contract address (hex)"},
				"slot":    {"type": "string",  "description": "Storage slot (hex)"},
				"block":   {"type": "integer", "description": "Block number (default: latest)"}
			},
			"required": ["address", "slot"]
		}`),
		Handler: s.toolGetStorageAt,
	})

	s.RegisterTool(Tool{
		Name:        "chainInfo",
		Description: "Get chain metadata: chainId, latest block, sync status, peer count.",
		Parameters:  json.RawMessage(`{"type": "object", "properties": {}}`),
		Handler:     s.toolChainInfo,
	})

	s.RegisterTool(Tool{
		Name:        "searchTransactions",
		Description: "Search recent transactions by from/to address within a block range.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address":   {"type": "string",  "description": "Address to search for (hex, matches from or to)"},
				"fromBlock": {"type": "integer", "description": "Start block number"},
				"toBlock":   {"type": "integer", "description": "End block number"},
				"limit":     {"type": "integer", "description": "Max results (default: 50, max: 200)"}
			},
			"required": ["address"]
		}`),
		Handler: s.toolSearchTransactions,
	})
}

// --- Tool parameter types ---

type getBlockParams struct {
	Number *uint64 `json:"number"`
	Hash   *string `json:"hash"`
	Full   bool    `json:"full"`
}

type getTransactionParams struct {
	Hash string `json:"hash"`
}

type getBalanceParams struct {
	Address string  `json:"address"`
	Block   *uint64 `json:"block"`
}

type getLogsParams struct {
	Address   *string  `json:"address"`
	FromBlock *uint64  `json:"fromBlock"`
	ToBlock   *uint64  `json:"toBlock"`
	Topics    []string `json:"topics"`
}

type getCodeParams struct {
	Address string  `json:"address"`
	Block   *uint64 `json:"block"`
}

type getStorageAtParams struct {
	Address string  `json:"address"`
	Slot    string  `json:"slot"`
	Block   *uint64 `json:"block"`
}

type searchTransactionsParams struct {
	Address   string  `json:"address"`
	FromBlock *uint64 `json:"fromBlock"`
	ToBlock   *uint64 `json:"toBlock"`
	Limit     *int    `json:"limit"`
}

// --- Tool result types ---

type blockResult struct {
	Number       uint64            `json:"number"`
	Hash         string            `json:"hash"`
	ParentHash   string            `json:"parent_hash"`
	StateRoot    string            `json:"state_root"`
	TxHash       string            `json:"tx_hash"`
	Coinbase     string            `json:"coinbase"`
	GasLimit     uint64            `json:"gas_limit"`
	GasUsed      uint64            `json:"gas_used"`
	Timestamp    uint64            `json:"timestamp"`
	Difficulty   string            `json:"difficulty"`
	BaseFee      string            `json:"base_fee,omitempty"`
	TxCount      int               `json:"tx_count"`
	Transactions interface{}       `json:"transactions,omitempty"`
}

type txSummary struct {
	Hash     string `json:"hash"`
	From     string `json:"from,omitempty"`
	To       string `json:"to,omitempty"`
	Value    string `json:"value"`
	Nonce    uint64 `json:"nonce"`
	Gas      uint64 `json:"gas"`
	GasPrice string `json:"gas_price,omitempty"`
	Type     uint8  `json:"type"`
}

type txResult struct {
	Hash      string `json:"hash"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Value     string `json:"value"`
	Nonce     uint64 `json:"nonce"`
	Gas       uint64 `json:"gas"`
	GasPrice  string `json:"gas_price,omitempty"`
	GasFeeCap string `json:"gas_fee_cap,omitempty"`
	GasTipCap string `json:"gas_tip_cap,omitempty"`
	Data      string `json:"data"`
	Type      uint8  `json:"type"`
	Block     uint64 `json:"block,omitempty"`
}

type logResult struct {
	Address     string   `json:"address"`
	Topics      []string `json:"topics"`
	Data        string   `json:"data"`
	BlockNumber uint64   `json:"block_number"`
	TxHash      string   `json:"tx_hash"`
	TxIndex     uint     `json:"tx_index"`
	LogIndex    uint     `json:"log_index"`
	Removed     bool     `json:"removed"`
}

// --- Tool handlers ---

func (s *Server) toolGetBlock(_ context.Context, params json.RawMessage) (interface{}, error) {
	var p getBlockParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	bc := s.backend.BlockChain()
	var blk block.IBlock
	var err error

	switch {
	case p.Hash != nil:
		h, herr := hashFromHex(*p.Hash)
		if herr != nil {
			return nil, fmt.Errorf("invalid hash: %w", herr)
		}
		blk, err = bc.GetBlockByHash(h)
	case p.Number != nil:
		n := uint256.NewInt(*p.Number)
		blk, err = bc.GetBlockByNumber(n)
	default:
		// Latest block.
		blk = bc.CurrentBlock()
	}

	if err != nil {
		return nil, fmt.Errorf("block not found: %w", err)
	}
	if blk == nil {
		return nil, fmt.Errorf("block not found")
	}

	return marshalBlock(blk, p.Full), nil
}

func (s *Server) toolGetTransaction(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p getTransactionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	txHash, err := hashFromHex(p.Hash)
	if err != nil {
		return nil, fmt.Errorf("invalid hash: %w", err)
	}

	// Search for the transaction in the tx pool first.
	pool := s.backend.TxPool()
	if pool != nil {
		if tx := pool.GetTx(txHash); tx != nil {
			return marshalTx(tx, 0), nil
		}
	}

	// Search recent blocks for the transaction.
	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()
	if current == nil {
		return nil, fmt.Errorf("transaction not found")
	}

	// Scan the last 128 blocks to limit DoS exposure.
	head := current.Number64().Uint64()
	const scanDepth = 128
	start := uint64(0)
	if head > scanDepth {
		start = head - scanDepth
	}

	log.Warn("MCP getTransaction: scanning blocks for tx (consider using tx index)", "hash", p.Hash, "depth", scanDepth)

	for i := head; i > start; i-- {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		blk, err := bc.GetBlockByNumber(uint256.NewInt(i))
		if err != nil || blk == nil {
			continue
		}
		for _, tx := range blk.Transactions() {
			if tx.Hash() == txHash {
				return marshalTx(tx, i), nil
			}
		}
	}

	return nil, fmt.Errorf("transaction not found: %s", p.Hash)
}

func (s *Server) toolGetBalance(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p getBalanceParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	addr, err := addressFromHex(p.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()
	if current == nil {
		return nil, fmt.Errorf("no current block available")
	}
	blockNr := current.Number64().Uint64()
	if p.Block != nil {
		blockNr = *p.Block
	}

	db := s.backend.Database()
	var balance string

	if err := db.View(ctx, func(tx kv.Tx) error {
		stateIface := bc.StateAt(tx, blockNr)
		if stateIface == nil {
			return fmt.Errorf("state not available at block %d", blockNr)
		}

		// Use the IStateDB interface to read balance.
		stateDB, ok := stateIface.(interface {
			GetBalance(types.Address) *uint256.Int
		})
		if !ok {
			return fmt.Errorf("state does not implement balance reader")
		}

		bal := stateDB.GetBalance(addr)
		if bal == nil {
			balance = "0"
		} else {
			balance = bal.ToBig().String()
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"address": addr.Hex(),
		"balance": balance,
		"block":   blockNr,
	}, nil
}

func (s *Server) toolGetLogs(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p getLogsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()
	if current == nil {
		return nil, fmt.Errorf("no current block")
	}

	head := current.Number64().Uint64()
	from := head
	to := head
	if p.FromBlock != nil {
		from = *p.FromBlock
	}
	if p.ToBlock != nil {
		to = *p.ToBlock
	}

	if from > to {
		return nil, fmt.Errorf("fromBlock (%d) must be <= toBlock (%d)", from, to)
	}

	// Limit range to prevent excessive scanning.
	const maxRange = 1000
	if to-from > maxRange {
		from = to - maxRange
	}

	var filterAddr *types.Address
	if p.Address != nil {
		a, err := addressFromHex(*p.Address)
		if err != nil {
			return nil, fmt.Errorf("invalid address: %w", err)
		}
		filterAddr = &a
	}

	filterTopics := make([]types.Hash, 0, len(p.Topics))
	for _, t := range p.Topics {
		h, err := hashFromHex(t)
		if err != nil {
			return nil, fmt.Errorf("invalid topic: %w", err)
		}
		filterTopics = append(filterTopics, h)
	}

	var results []logResult
	const maxResults = 500

	for i := from; i <= to && len(results) < maxResults; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		blk, err := bc.GetBlockByNumber(uint256.NewInt(i))
		if err != nil || blk == nil {
			continue
		}

		logs, err := bc.GetLogs(blk.Hash())
		if err != nil {
			continue
		}

		for _, txLogs := range logs {
			for _, l := range txLogs {
				if !matchLog(l, filterAddr, filterTopics) {
					continue
				}
				results = append(results, marshalLog(l))
				if len(results) >= maxResults {
					break
				}
			}
		}
	}

	return map[string]interface{}{
		"logs":  results,
		"count": len(results),
	}, nil
}

func (s *Server) toolGetCode(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p getCodeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	addr, err := addressFromHex(p.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()
	if current == nil {
		return nil, fmt.Errorf("no current block available")
	}
	blockNr := current.Number64().Uint64()
	if p.Block != nil {
		blockNr = *p.Block
	}

	db := s.backend.Database()
	var code string

	if err := db.View(ctx, func(tx kv.Tx) error {
		stateIface := bc.StateAt(tx, blockNr)
		if stateIface == nil {
			return fmt.Errorf("state not available at block %d", blockNr)
		}

		stateDB, ok := stateIface.(interface {
			GetCode(types.Address) []byte
		})
		if !ok {
			return fmt.Errorf("state does not implement code reader")
		}

		codeBytes := stateDB.GetCode(addr)
		code = fmt.Sprintf("0x%x", codeBytes)
		return nil
	}); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"address": addr.Hex(),
		"code":    code,
		"block":   blockNr,
	}, nil
}

func (s *Server) toolGetStorageAt(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p getStorageAtParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	addr, err := addressFromHex(p.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	slot, err := hashFromHex(p.Slot)
	if err != nil {
		return nil, fmt.Errorf("invalid slot: %w", err)
	}

	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()
	if current == nil {
		return nil, fmt.Errorf("no current block available")
	}
	blockNr := current.Number64().Uint64()
	if p.Block != nil {
		blockNr = *p.Block
	}

	db := s.backend.Database()
	var value string

	if err := db.View(ctx, func(tx kv.Tx) error {
		stateIface := bc.StateAt(tx, blockNr)
		if stateIface == nil {
			return fmt.Errorf("state not available at block %d", blockNr)
		}

		stateDB, ok := stateIface.(interface {
			GetState(types.Address, *types.Hash, *uint256.Int)
		})
		if !ok {
			return fmt.Errorf("state does not implement storage reader")
		}

		var val uint256.Int
		stateDB.GetState(addr, &slot, &val)
		value = fmt.Sprintf("0x%x", val.Bytes32())
		return nil
	}); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"address": addr.Hex(),
		"slot":    slot.Hex(),
		"value":   value,
		"block":   blockNr,
	}, nil
}

func (s *Server) toolChainInfo(_ context.Context, _ json.RawMessage) (interface{}, error) {
	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()

	result := map[string]interface{}{
		"peer_count": s.backend.PeerCount(),
	}

	if cfg := bc.Config(); cfg != nil && cfg.ChainID != nil {
		result["chain_id"] = cfg.ChainID.Uint64()
		result["chain_name"] = cfg.ChainName
	}

	if current != nil {
		result["latest_block"] = current.Number64().Uint64()
		result["latest_hash"] = current.Hash().Hex()
		result["latest_timestamp"] = current.Time()
	}

	if status := s.backend.SyncProgress(); status != nil {
		result["sync_status"] = status
	}

	return result, nil
}

func (s *Server) toolSearchTransactions(ctx context.Context, params json.RawMessage) (interface{}, error) {
	var p searchTransactionsParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	addr, err := addressFromHex(p.Address)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	bc := s.backend.BlockChain()
	current := bc.CurrentBlock()
	if current == nil {
		return nil, fmt.Errorf("no current block")
	}

	head := current.Number64().Uint64()
	from := head
	to := head

	if p.FromBlock != nil {
		from = *p.FromBlock
	}
	if p.ToBlock != nil {
		to = *p.ToBlock
	}

	// Default: search last 100 blocks if no range specified.
	if p.FromBlock == nil && p.ToBlock == nil {
		if head > 100 {
			from = head - 100
		} else {
			from = 0
		}
		to = head
	}

	if from > to {
		return nil, fmt.Errorf("fromBlock (%d) must be <= toBlock (%d)", from, to)
	}

	// Limit scan range.
	const maxScanRange = 500
	if to-from > maxScanRange {
		from = to - maxScanRange
	}

	limit := 50
	if p.Limit != nil {
		limit = *p.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
	}

	var results []map[string]interface{}

	for i := to; i >= from && len(results) < limit; i-- {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		blk, err := bc.GetBlockByNumber(uint256.NewInt(i))
		if err != nil || blk == nil {
			if i == 0 {
				break
			}
			continue
		}

		for _, tx := range blk.Transactions() {
			matched := false
			if from := tx.From(); from != nil && *from == addr {
				matched = true
			}
			if to := tx.To(); to != nil && *to == addr {
				matched = true
			}

			if matched {
				txValue := "0"
				if v := tx.Value(); v != nil {
					txValue = v.ToBig().String()
				}
				results = append(results, map[string]interface{}{
					"hash":  tx.Hash().Hex(),
					"block": i,
					"from":  addrStr(tx.From()),
					"to":    addrStr(tx.To()),
					"value": txValue,
					"nonce": tx.Nonce(),
				})
				if len(results) >= limit {
					break
				}
			}
		}

		if i == 0 {
			break
		}
	}

	return map[string]interface{}{
		"address":      addr.Hex(),
		"transactions": results,
		"count":        len(results),
		"scanned_from": from,
		"scanned_to":   to,
	}, nil
}

// --- Helper functions ---

// marshalBlock converts a block to a JSON-friendly result.
func marshalBlock(blk block.IBlock, full bool) *blockResult {
	result := &blockResult{
		Number:     blk.Number64().Uint64(),
		Hash:       blk.Hash().Hex(),
		ParentHash: blk.ParentHash().Hex(),
		StateRoot:  blk.StateRoot().Hex(),
		TxHash:     blk.TxHash().Hex(),
		Coinbase:   blk.Coinbase().Hex(),
		GasLimit:   blk.GasLimit(),
		GasUsed:    blk.GasUsed(),
		Timestamp:  blk.Time(),
		TxCount:    len(blk.Transactions()),
	}

	if d := blk.Difficulty(); d != nil {
		result.Difficulty = d.ToBig().String()
	} else {
		result.Difficulty = "0"
	}

	if bf := blk.BaseFee64(); bf != nil {
		result.BaseFee = bf.ToBig().String()
	}

	txs := blk.Transactions()
	if full && len(txs) > 0 {
		fullTxs := make([]txSummary, 0, len(txs))
		for _, tx := range txs {
			fullTxs = append(fullTxs, txSummaryFromTx(tx))
		}
		result.Transactions = fullTxs
	} else if len(txs) > 0 {
		hashes := make([]string, 0, len(txs))
		for _, tx := range txs {
			hashes = append(hashes, tx.Hash().Hex())
		}
		result.Transactions = hashes
	}

	return result
}

// txSummaryFromTx creates a transaction summary from a transaction.
func txSummaryFromTx(tx *transaction.Transaction) txSummary {
	valueStr := "0"
	if v := tx.Value(); v != nil {
		valueStr = v.ToBig().String()
	}
	s := txSummary{
		Hash:  tx.Hash().Hex(),
		Value: valueStr,
		Nonce: tx.Nonce(),
		Gas:   tx.Gas(),
		Type:  tx.Type(),
	}

	if from := tx.From(); from != nil {
		s.From = from.Hex()
	}
	if to := tx.To(); to != nil {
		s.To = to.Hex()
	}
	if gp := tx.GasPrice(); gp != nil {
		s.GasPrice = gp.ToBig().String()
	}

	return s
}

// marshalTx converts a transaction to a detailed result.
func marshalTx(tx *transaction.Transaction, blockNum uint64) *txResult {
	valueStr := "0"
	if v := tx.Value(); v != nil {
		valueStr = v.ToBig().String()
	}
	r := &txResult{
		Hash:  tx.Hash().Hex(),
		Value: valueStr,
		Nonce: tx.Nonce(),
		Gas:   tx.Gas(),
		Data:  fmt.Sprintf("0x%x", tx.Data()),
		Type:  tx.Type(),
		Block: blockNum,
	}

	if from := tx.From(); from != nil {
		r.From = from.Hex()
	}
	if to := tx.To(); to != nil {
		r.To = to.Hex()
	}
	if gp := tx.GasPrice(); gp != nil {
		r.GasPrice = gp.ToBig().String()
	}
	if fc := tx.GasFeeCap(); fc != nil {
		r.GasFeeCap = fc.ToBig().String()
	}
	if tc := tx.GasTipCap(); tc != nil {
		r.GasTipCap = tc.ToBig().String()
	}

	return r
}

// marshalLog converts a Log to a logResult.
func marshalLog(l *block.Log) logResult {
	topics := make([]string, len(l.Topics))
	for i, t := range l.Topics {
		topics[i] = t.Hex()
	}

	blockNum := uint64(0)
	if l.BlockNumber != nil {
		blockNum = l.BlockNumber.Uint64()
	}

	return logResult{
		Address:     l.Address.Hex(),
		Topics:      topics,
		Data:        fmt.Sprintf("0x%x", l.Data),
		BlockNumber: blockNum,
		TxHash:      l.TxHash.Hex(),
		TxIndex:     l.TxIndex,
		LogIndex:    l.Index,
		Removed:     l.Removed,
	}
}

// matchLog checks whether a log matches the given address and topic filters.
func matchLog(l *block.Log, addr *types.Address, topics []types.Hash) bool {
	if addr != nil && l.Address != *addr {
		return false
	}
	for i, topic := range topics {
		if topic == (types.Hash{}) {
			continue // wildcard
		}
		if i >= len(l.Topics) || l.Topics[i] != topic {
			return false
		}
	}
	return true
}

// addrStr returns the hex string of an address pointer, or empty string if nil.
func addrStr(a *types.Address) string {
	if a == nil {
		return ""
	}
	return a.Hex()
}
