package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	chain "github.com/n42blockchain/N42/common/types"
	d "github.com/n42blockchain/N42/internal/ddn/types"
	"github.com/n42blockchain/N42/internal/mcp"
)

func decode(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

// RegisterMCPTools uses the existing MCP server's configured tool allowlist.
func RegisterMCPTools(s *mcp.Server, g *Gateway) {
	s.RegisterTool(mcp.Tool{Name: "ddn.info", Description: "Read active executable model identity, quorum and ordered native labels.", Parameters: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), Handler: func(ctx context.Context, data json.RawMessage) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var p struct{}
		if err := decode(data, &p); err != nil {
			return nil, err
		}
		return g.Info(), nil
	}})

	s.RegisterTool(mcp.Tool{Name: "ddn.decide", Description: "Submit a redacted public decision in shadow mode; returns a request ID. Output never controls node execution.", Parameters: json.RawMessage(`{"type":"object","properties":{"request":{"type":"object"},"input":{"type":"string"}},"required":["request","input"],"additionalProperties":false}`), Handler: func(ctx context.Context, data json.RawMessage) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var p struct {
			Request d.DecisionRequest `json:"request"`
			Input   string            `json:"input"`
		}
		if err := decode(data, &p); err != nil {
			return nil, err
		}
		return g.Submit(p.Request, p.Input)
	}})
	s.RegisterTool(mcp.Tool{Name: "ddn.getReceipt", Description: "Read a shadow-mode decision receipt or pending status.", Parameters: json.RawMessage(`{"type":"object","properties":{"request_id":{"type":"string"}},"required":["request_id"],"additionalProperties":false}`), Handler: func(ctx context.Context, data json.RawMessage) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var p struct {
			ID chain.Hash `json:"request_id"`
		}
		if err := decode(data, &p); err != nil {
			return nil, err
		}
		return g.GetReceipt(p.ID)
	}})
}
