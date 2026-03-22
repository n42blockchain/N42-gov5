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

	"github.com/n42blockchain/N42/common/types"
)

// AIIndexProvider is the interface implemented by the AI indexer to expose
// indexed blockchain data to MCP data query tools. Each method returns
// interface{} so results can be directly serialized to JSON.
type AIIndexProvider interface {
	QueryTransfers(contract *types.Address, from *types.Address, to *types.Address, fromBlock, toBlock uint64, limit int) interface{}
	GetProfile(addr types.Address) interface{}
	QueryEvents(contract *types.Address, eventSig *types.Hash, fromBlock, toBlock uint64, limit int) interface{}
	QueryGasMetrics(fromBlock, toBlock uint64) interface{}
}

// registerDataTools registers the AI data query tools with the MCP server.
// These tools enable AI agents to query token transfers, address profiles,
// contract events, and gas analytics from the indexed blockchain data.
func (s *Server) registerDataTools(indexer AIIndexProvider) {
	s.RegisterTool(Tool{
		Name:        "queryTokenTransfers",
		Description: "Query indexed ERC20/ERC721 token transfer events. Filter by contract address, sender, recipient, and block range. Returns most recent transfers first.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"contract":  {"type": "string",  "description": "Filter by token contract address (hex)"},
				"from":      {"type": "string",  "description": "Filter by sender address (hex)"},
				"to":        {"type": "string",  "description": "Filter by recipient address (hex)"},
				"fromBlock": {"type": "integer", "description": "Start block number (inclusive)"},
				"toBlock":   {"type": "integer", "description": "End block number (inclusive)"},
				"limit":     {"type": "integer", "description": "Max results (default: 100, max: 1000)"}
			}
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p queryTokenTransfersParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}

			var contract *types.Address
			if p.Contract != nil {
				a, err := addressFromHex(*p.Contract)
				if err != nil {
					return nil, fmt.Errorf("invalid contract address: %w", err)
				}
				contract = &a
			}

			var from *types.Address
			if p.From != nil {
				a, err := addressFromHex(*p.From)
				if err != nil {
					return nil, fmt.Errorf("invalid from address: %w", err)
				}
				from = &a
			}

			var to *types.Address
			if p.To != nil {
				a, err := addressFromHex(*p.To)
				if err != nil {
					return nil, fmt.Errorf("invalid to address: %w", err)
				}
				to = &a
			}

			var fromBlock, toBlock uint64
			if p.FromBlock != nil {
				fromBlock = *p.FromBlock
			}
			if p.ToBlock != nil {
				toBlock = *p.ToBlock
			}

			limit := 100
			if p.Limit != nil {
				limit = *p.Limit
				if limit <= 0 {
					limit = 100
				}
				if limit > 1000 {
					limit = 1000
				}
			}

			result := indexer.QueryTransfers(contract, from, to, fromBlock, toBlock, limit)
			return map[string]interface{}{
				"transfers": result,
			}, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "getAddressProfile",
		Description: "Get activity profile for an address including transaction count, last active block, and contract call count.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address": {"type": "string", "description": "Account address (hex)"}
			},
			"required": ["address"]
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p getAddressProfileParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}

			if p.Address == "" {
				return nil, fmt.Errorf("address is required")
			}

			addr, err := addressFromHex(p.Address)
			if err != nil {
				return nil, fmt.Errorf("invalid address: %w", err)
			}

			profile := indexer.GetProfile(addr)
			if profile == nil {
				return map[string]interface{}{
					"address": addr.Hex(),
					"found":   false,
					"message": "no activity recorded for this address",
				}, nil
			}

			return map[string]interface{}{
				"address": addr.Hex(),
				"found":   true,
				"profile": profile,
			}, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "analyzeContract",
		Description: "Analyze a contract's event activity. Returns all indexed events for the contract within the specified block range, plus summary statistics.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address":   {"type": "string",  "description": "Contract address (hex)"},
				"fromBlock": {"type": "integer", "description": "Start block number (inclusive)"},
				"toBlock":   {"type": "integer", "description": "End block number (inclusive)"}
			},
			"required": ["address"]
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p analyzeContractParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}

			if p.Address == "" {
				return nil, fmt.Errorf("address is required")
			}

			addr, err := addressFromHex(p.Address)
			if err != nil {
				return nil, fmt.Errorf("invalid address: %w", err)
			}

			var fromBlock, toBlock uint64
			if p.FromBlock != nil {
				fromBlock = *p.FromBlock
			}
			if p.ToBlock != nil {
				toBlock = *p.ToBlock
			}

			events := indexer.QueryEvents(&addr, nil, fromBlock, toBlock, 1000)

			// Compute event signature distribution.
			sigCounts := make(map[string]int)
			eventsJSON, marshalErr := json.Marshal(events)
			var eventArray []struct {
				EventSig string `json:"EventSig"`
			}
			if marshalErr == nil {
				_ = json.Unmarshal(eventsJSON, &eventArray)
			}

			eventCount := len(eventArray)
			for _, e := range eventArray {
				if e.EventSig != "" {
					sigCounts[e.EventSig]++
				}
			}

			// Get the contract's address profile.
			profile := indexer.GetProfile(addr)

			// Also query token transfers for this contract.
			transfers := indexer.QueryTransfers(&addr, nil, nil, fromBlock, toBlock, 100)

			return map[string]interface{}{
				"address":         addr.Hex(),
				"events":          events,
				"event_count":     eventCount,
				"event_signatures": sigCounts,
				"transfers":       transfers,
				"profile":         profile,
			}, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "getGasAnalytics",
		Description: "Get gas usage analytics for a block range. Returns per-block gas metrics including utilization, base fee trends, and transaction counts.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"fromBlock": {"type": "integer", "description": "Start block number (inclusive)"},
				"toBlock":   {"type": "integer", "description": "End block number (inclusive)"}
			}
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p getGasAnalyticsParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}

			var fromBlock, toBlock uint64
			if p.FromBlock != nil {
				fromBlock = *p.FromBlock
			}
			if p.ToBlock != nil {
				toBlock = *p.ToBlock
			}

			metrics := indexer.QueryGasMetrics(fromBlock, toBlock)

			// Compute aggregate statistics.
			metricsJSON, marshalErr := json.Marshal(metrics)
			var metricArray []struct {
				GasUsed     uint64  `json:"GasUsed"`
				GasLimit    uint64  `json:"GasLimit"`
				TxCount     int     `json:"TxCount"`
				Utilization float64 `json:"Utilization"`
			}
			if marshalErr == nil {
				_ = json.Unmarshal(metricsJSON, &metricArray)
			}

			count := len(metricArray)
			var totalGasUsed, totalGasLimit uint64
			var totalTxCount int
			var totalUtilization float64

			for _, m := range metricArray {
				totalGasUsed += m.GasUsed
				totalGasLimit += m.GasLimit
				totalTxCount += m.TxCount
				totalUtilization += m.Utilization
			}

			var avgUtilization float64
			var avgTxCount float64
			if count > 0 {
				avgUtilization = totalUtilization / float64(count)
				avgTxCount = float64(totalTxCount) / float64(count)
			}

			return map[string]interface{}{
				"metrics":     metrics,
				"block_count": count,
				"summary": map[string]interface{}{
					"total_gas_used":     totalGasUsed,
					"total_gas_limit":    totalGasLimit,
					"total_tx_count":     totalTxCount,
					"avg_utilization":    avgUtilization,
					"avg_tx_per_block":   avgTxCount,
				},
			}, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "queryEvents",
		Description: "Query indexed contract events with structured filters. Filter by contract address, event signature, and block range.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address":        {"type": "string",  "description": "Contract address to filter (hex)"},
				"eventSignature": {"type": "string",  "description": "Event signature hash / topic[0] (hex)"},
				"fromBlock":      {"type": "integer", "description": "Start block number (inclusive)"},
				"toBlock":        {"type": "integer", "description": "End block number (inclusive)"},
				"limit":          {"type": "integer", "description": "Max results (default: 100, max: 1000)"}
			}
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p queryEventsParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}

			var contract *types.Address
			if p.Address != nil {
				a, err := addressFromHex(*p.Address)
				if err != nil {
					return nil, fmt.Errorf("invalid address: %w", err)
				}
				contract = &a
			}

			var eventSig *types.Hash
			if p.EventSignature != nil {
				h, err := hashFromHex(*p.EventSignature)
				if err != nil {
					return nil, fmt.Errorf("invalid event signature: %w", err)
				}
				eventSig = &h
			}

			var fromBlock, toBlock uint64
			if p.FromBlock != nil {
				fromBlock = *p.FromBlock
			}
			if p.ToBlock != nil {
				toBlock = *p.ToBlock
			}

			limit := 100
			if p.Limit != nil {
				limit = *p.Limit
				if limit <= 0 {
					limit = 100
				}
				if limit > 1000 {
					limit = 1000
				}
			}

			events := indexer.QueryEvents(contract, eventSig, fromBlock, toBlock, limit)
			return map[string]interface{}{
				"events": events,
			}, nil
		},
	})
}

// --- Data tool parameter types ---

type queryTokenTransfersParams struct {
	Contract  *string `json:"contract"`
	From      *string `json:"from"`
	To        *string `json:"to"`
	FromBlock *uint64 `json:"fromBlock"`
	ToBlock   *uint64 `json:"toBlock"`
	Limit     *int    `json:"limit"`
}

type getAddressProfileParams struct {
	Address string `json:"address"`
}

type analyzeContractParams struct {
	Address   string  `json:"address"`
	FromBlock *uint64 `json:"fromBlock"`
	ToBlock   *uint64 `json:"toBlock"`
}

type getGasAnalyticsParams struct {
	FromBlock *uint64 `json:"fromBlock"`
	ToBlock   *uint64 `json:"toBlock"`
}

type queryEventsParams struct {
	Address        *string `json:"address"`
	EventSignature *string `json:"eventSignature"`
	FromBlock      *uint64 `json:"fromBlock"`
	ToBlock        *uint64 `json:"toBlock"`
	Limit          *int    `json:"limit"`
}
