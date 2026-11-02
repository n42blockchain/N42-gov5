// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package packetdtest is an in-process JSON-RPC node over gov5 types, the
// subset of the eth/debug namespaces the packet sidecar (packetd) uses,
// answered the way reth answers them. Tests drive the sidecar against it
// without a live n42-rs node.
package packetdtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules/state"
)

// Block is one block the node serves.
type Block struct {
	Header *block.Header
	Txs    transaction.Transactions
}

// Node serves blocks and the state of one block.
type Node struct {
	mu sync.Mutex
	// Blocks by number; Blocks[i].Header.Number must be i + First.
	Blocks []Block
	First  uint64
	// State answers state queries at StateBlock (by hash, EIP-1898) or
	// StateNumber; a query at any other block is refused.
	State       state.StateReader
	StateBlock  types.Hash
	StateNumber uint64

	// NoRawBlock / NoRawHeader answer debug_getRawBlock / debug_getRawHeader
	// with -32601, as reth does when the debug namespace is not enabled.
	NoRawBlock  bool
	NoRawHeader bool
	// TamperRaw serves raw headers with one extra byte of Extra, so they no
	// longer hash to the block hash the node reports.
	TamperRaw bool

	// Calls counts calls by method.
	Calls map[string]int
}

type request struct {
	ID     json.RawMessage   `json:"id"`
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

// ServeHTTP answers single and batch JSON-RPC requests.
func (n *Node) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		var reqs []request
		if err := json.Unmarshal(body, &reqs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out := make([]response, len(reqs))
		for i, q := range reqs {
			out[i] = n.answer(q)
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	var q request
	if err := json.Unmarshal(body, &q); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(n.answer(q))
}

func (n *Node) answer(q request) response {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Calls == nil {
		n.Calls = make(map[string]int)
	}
	n.Calls[q.Method]++
	res, err := n.dispatch(q.Method, q.Params)
	out := response{JSONRPC: "2.0", ID: q.ID}
	if err != nil {
		out.Error = err
	} else if res == nil {
		out.Result = json.RawMessage("null")
	} else {
		out.Result = res
	}
	return out
}

func str(p []json.RawMessage, i int) string {
	if i >= len(p) {
		return ""
	}
	var s string
	_ = json.Unmarshal(p[i], &s)
	return s
}

func (n *Node) blockByID(id string) *Block {
	if len(id) == 66 {
		h := types.HexToHash(id)
		for i := range n.Blocks {
			if n.Blocks[i].Header.Hash() == h {
				return &n.Blocks[i]
			}
		}
		return nil
	}
	num, err := hexutil.DecodeUint64(id)
	if err != nil || num < n.First || num-n.First >= uint64(len(n.Blocks)) {
		return nil
	}
	return &n.Blocks[num-n.First]
}

func notFound(m string) *rpcErr {
	return &rpcErr{Code: -32601, Message: fmt.Sprintf("the method %s does not exist/is not available", m)}
}

func (n *Node) dispatch(m string, p []json.RawMessage) (any, *rpcErr) {
	switch m {
	case "eth_blockNumber":
		if len(n.Blocks) == 0 {
			return hexutil.EncodeUint64(0), nil
		}
		return hexutil.EncodeUint64(n.First + uint64(len(n.Blocks)) - 1), nil
	case "eth_getBlockByNumber", "eth_getBlockByHash":
		b := n.blockByID(str(p, 0))
		if b == nil {
			return nil, nil
		}
		return blockJSON(b), nil
	case "debug_getRawBlock":
		if n.NoRawBlock {
			return nil, notFound(m)
		}
		b := n.blockByID(str(p, 0))
		if b == nil {
			return nil, &rpcErr{Code: -32001, Message: "block not found"}
		}
		enc, err := EncodeEthBlock(n.served(b))
		if err != nil {
			return nil, &rpcErr{Code: -32603, Message: err.Error()}
		}
		return hexutil.Encode(enc), nil
	case "debug_getRawHeader":
		if n.NoRawHeader {
			return nil, notFound(m)
		}
		b := n.blockByID(str(p, 0))
		if b == nil {
			return nil, &rpcErr{Code: -32001, Message: "block not found"}
		}
		enc, err := rlp.EncodeToBytes(n.served(b).Header)
		if err != nil {
			return nil, &rpcErr{Code: -32603, Message: err.Error()}
		}
		return hexutil.Encode(enc), nil
	case "eth_getRawTransactionByBlockHashAndIndex":
		b := n.blockByID(str(p, 0))
		i, err := hexutil.DecodeUint64(str(p, 1))
		if b == nil || err != nil || i >= uint64(len(b.Txs)) {
			return nil, nil
		}
		enc, eerr := b.Txs[i].EthEncoded()
		if eerr != nil {
			return nil, &rpcErr{Code: -32603, Message: eerr.Error()}
		}
		return hexutil.Encode(enc), nil
	case "eth_getTransactionCount", "eth_getBalance", "eth_getCode":
		if e := n.checkStateBlock(p, 1); e != nil {
			return nil, e
		}
		addr := types.HexToAddress(str(p, 0))
		acc, err := n.State.ReadAccountData(addr)
		if err != nil {
			return nil, &rpcErr{Code: -32000, Message: err.Error()}
		}
		switch m {
		case "eth_getTransactionCount":
			if acc == nil {
				return "0x0", nil
			}
			return hexutil.EncodeUint64(acc.Nonce), nil
		case "eth_getBalance":
			if acc == nil {
				return "0x0", nil
			}
			return hexutil.EncodeBig(acc.Balance.ToBig()), nil
		default:
			if acc == nil {
				return "0x", nil
			}
			code, err := n.State.ReadAccountCode(addr, acc.CodeHash)
			if err != nil {
				return nil, &rpcErr{Code: -32000, Message: err.Error()}
			}
			return hexutil.Encode(code), nil
		}
	case "eth_getStorageAt":
		if e := n.checkStateBlock(p, 2); e != nil {
			return nil, e
		}
		addr := types.HexToAddress(str(p, 0))
		slot := types.HexToHash(str(p, 1))
		v, err := n.State.ReadAccountStorage(addr, &slot)
		if err != nil {
			return nil, &rpcErr{Code: -32000, Message: err.Error()}
		}
		return types.BytesToHash(v).Hex(), nil // 32 bytes, as reth answers
	}
	return nil, notFound(m)
}

// served is b as its raw encodings are served (TamperRaw).
func (n *Node) served(b *Block) *Block {
	if !n.TamperRaw {
		return b
	}
	h := block.CopyHeader(b.Header)
	h.Extra = append(append([]byte(nil), h.Extra...), 0)
	h.ResetHashCache()
	return &Block{Header: h, Txs: b.Txs}
}

// checkStateBlock refuses a state query at any block but the served one.
func (n *Node) checkStateBlock(p []json.RawMessage, i int) *rpcErr {
	if i >= len(p) {
		return &rpcErr{Code: -32602, Message: "missing block parameter"}
	}
	var obj struct {
		BlockHash string `json:"blockHash"`
	}
	if json.Unmarshal(p[i], &obj) == nil && obj.BlockHash != "" {
		if types.HexToHash(obj.BlockHash) != n.StateBlock {
			return &rpcErr{Code: -32000, Message: "state at " + obj.BlockHash + " not served"}
		}
		return nil
	}
	if num, err := hexutil.DecodeUint64(str(p, i)); err == nil && num == n.StateNumber {
		return nil
	}
	return &rpcErr{Code: -32000, Message: "state at " + string(p[i]) + " not served"}
}

// EncodeEthBlock encodes b as an Ethereum-form block, the bytes reth's
// debug_getRawBlock answers: [header, [tx...], [] ommers, [] withdrawals].
// A typed transaction is a string, a legacy one an embedded list.
func EncodeEthBlock(b *Block) ([]byte, error) {
	txs := make([]rlp.RawValue, len(b.Txs))
	for i, tx := range b.Txs {
		enc, err := tx.EthEncoded()
		if err != nil {
			return nil, err
		}
		if len(enc) > 0 && enc[0] >= 0xc0 {
			txs[i] = rlp.RawValue(enc)
			continue
		}
		s, err := rlp.EncodeToBytes(enc)
		if err != nil {
			return nil, err
		}
		txs[i] = s
	}
	return rlp.EncodeToBytes([]any{b.Header, txs, []rlp.RawValue{}, []rlp.RawValue{}})
}

func hp(h *types.Hash) any {
	if h == nil {
		return nil
	}
	return h.Hex()
}

func up(v *uint64) any {
	if v == nil {
		return nil
	}
	return hexutil.EncodeUint64(*v)
}

// blockJSON renders the header fields as reth's eth_getBlockByNumber does.
func blockJSON(b *Block) map[string]any {
	h := b.Header
	txs := make([]string, len(b.Txs))
	for i, tx := range b.Txs {
		txs[i] = tx.Hash().Hex()
	}
	m := map[string]any{
		"hash":             h.Hash().Hex(),
		"parentHash":       h.ParentHash.Hex(),
		"sha3Uncles":       h.UncleHash.Hex(),
		"miner":            h.Coinbase.Hex(),
		"stateRoot":        h.Root.Hex(),
		"transactionsRoot": h.TxHash.Hex(),
		"receiptsRoot":     h.ReceiptHash.Hex(),
		"logsBloom":        hexutil.Encode(h.Bloom[:]),
		"difficulty":       hexutil.EncodeBig(h.Difficulty.ToBig()),
		"number":           hexutil.EncodeBig(h.Number.ToBig()),
		"gasLimit":         hexutil.EncodeUint64(h.GasLimit),
		"gasUsed":          hexutil.EncodeUint64(h.GasUsed),
		"timestamp":        hexutil.EncodeUint64(h.Time),
		"extraData":        hexutil.Encode(h.Extra),
		"mixHash":          h.MixDigest.Hex(),
		"nonce":            hexutil.Encode(h.Nonce[:]),
		"transactions":     txs,
	}
	if h.BaseFee != nil {
		m["baseFeePerGas"] = hexutil.EncodeBig(h.BaseFee.ToBig())
	}
	for k, v := range map[string]any{
		"withdrawalsRoot":       hp(h.WithdrawalsHash),
		"blobGasUsed":           up(h.BlobGasUsed),
		"excessBlobGas":         up(h.ExcessBlobGas),
		"parentBeaconBlockRoot": hp(h.ParentBeaconRoot),
		"requestsHash":          hp(h.RequestsHash),
		"blockAccessListHash":   hp(h.BlockAccessListHash),
		"slotNumber":            up(h.SlotNumber),
		"mobileRegistryRoot":    hp(h.MobileRegistryRoot),
	} {
		if v != nil {
			m[k] = v
		}
	}
	return m
}
