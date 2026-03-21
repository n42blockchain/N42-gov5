// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// AgentProvider is the interface for agent discovery and coordination
// operations exposed through MCP tools. Implementations must be safe
// for concurrent use.
type AgentProvider interface {
	// FindAgents returns agents matching the given capability with reputation
	// at or above minReputation. The result is sorted by reputation descending.
	FindAgents(capability string, minReputation float64) interface{}

	// RequestTask creates a task negotiation for the given capability.
	// Returns the negotiation ID as a hex string.
	RequestTask(requesterDID string, capability string, inputCAS string, maxBudget string) (string, error)

	// CheckTaskStatus returns the current status of a negotiation.
	CheckTaskStatus(negotiationID string) (interface{}, error)

	// GetReputation returns the reputation details for an agent.
	GetReputation(did string) interface{}
}

// registerAgentTools registers MCP tools for AI agent discovery and
// coordination. These tools allow AI agents to find other agents,
// request tasks, check task status, and query reputation.
func (s *Server) registerAgentTools(provider AgentProvider) {
	s.RegisterTool(Tool{
		Name:        "findAgents",
		Description: "Find AI agents by capability. Returns matching agents sorted by reputation (highest first). Use this to discover agents that can perform specific tasks.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"capability": {
					"type": "string",
					"description": "Required capability to search for (e.g., 'inference', 'compute', 'storage')"
				},
				"minReputation": {
					"type": "number",
					"description": "Minimum reputation score (0-100, default: 0)"
				}
			},
			"required": ["capability"]
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p struct {
				Capability    string  `json:"capability"`
				MinReputation float64 `json:"minReputation"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
			if p.Capability == "" {
				return nil, fmt.Errorf("capability is required")
			}
			if p.MinReputation < 0 {
				p.MinReputation = 0
			}
			if p.MinReputation > 100 {
				p.MinReputation = 100
			}

			result := provider.FindAgents(p.Capability, p.MinReputation)
			return result, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "requestAgentTask",
		Description: "Create a task negotiation request for AI agents. Specify the required capability, input data (CAS hash), and maximum budget. Returns a negotiation ID that can be used to track bids and status.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"requesterDID": {
					"type": "string",
					"description": "DID of the requesting agent"
				},
				"capability": {
					"type": "string",
					"description": "Required capability for the task"
				},
				"inputCAS": {
					"type": "string",
					"description": "CAS hash of the input data (hex)"
				},
				"maxBudget": {
					"type": "string",
					"description": "Maximum budget in wei (decimal string)"
				}
			},
			"required": ["requesterDID", "capability", "inputCAS", "maxBudget"]
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p struct {
				RequesterDID string `json:"requesterDID"`
				Capability   string `json:"capability"`
				InputCAS     string `json:"inputCAS"`
				MaxBudget    string `json:"maxBudget"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
			if p.RequesterDID == "" {
				return nil, fmt.Errorf("requesterDID is required")
			}
			if p.Capability == "" {
				return nil, fmt.Errorf("capability is required")
			}
			if p.InputCAS == "" {
				return nil, fmt.Errorf("inputCAS is required")
			}
			if p.MaxBudget == "" {
				return nil, fmt.Errorf("maxBudget is required")
			}

			negID, err := provider.RequestTask(p.RequesterDID, p.Capability, p.InputCAS, p.MaxBudget)
			if err != nil {
				return nil, fmt.Errorf("failed to create task: %w", err)
			}

			return map[string]string{
				"negotiationID": negID,
				"status":        "open",
			}, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "checkTaskStatus",
		Description: "Check the current status of a task negotiation. Returns the negotiation details including status, bids received, accepted bid, and result if completed.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"negotiationID": {
					"type": "string",
					"description": "Negotiation ID (hex hash) returned by requestAgentTask"
				}
			},
			"required": ["negotiationID"]
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p struct {
				NegotiationID string `json:"negotiationID"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
			if p.NegotiationID == "" {
				return nil, fmt.Errorf("negotiationID is required")
			}

			result, err := provider.CheckTaskStatus(p.NegotiationID)
			if err != nil {
				return nil, fmt.Errorf("failed to check task: %w", err)
			}

			return result, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "getAgentReputation",
		Description: "Get the reputation details for an AI agent. Returns completion rate, response time, dispute rate, stake quality, and overall score.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"did": {
					"type": "string",
					"description": "DID of the agent to query"
				}
			},
			"required": ["did"]
		}`),
		Handler: func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p struct {
				DID string `json:"did"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
			if p.DID == "" {
				return nil, fmt.Errorf("did is required")
			}

			result := provider.GetReputation(p.DID)
			return result, nil
		},
	})
}
