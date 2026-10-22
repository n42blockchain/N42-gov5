package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type Client struct {
	identity Identity
	http     *http.Client
}

func NewClient(socket string, identity Identity) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &Client{identity: identity, http: &http.Client{Transport: transport, Timeout: 30 * time.Second}}
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) Identity() Identity { return c.identity }

func (c *Client) Import(ctx context.Context, request ImportRequest) (out ImportResult, err error) {
	phases := newOwnerOperationPhases("client_import", nil)
	phases.identity = c.identity
	defer func() { phases.finish(err) }()
	phases.phase("request_validate")
	if request.Identity != c.identity {
		return ImportResult{}, errors.New("client execution identity mismatch")
	}
	if len(request.BlockRLP) == 0 || len(request.BlockRLP) > MaxBlockBytes {
		return ImportResult{}, errors.New("invalid client execution block size")
	}
	var requested block.Block
	if err := requested.Unmarshal(request.BlockRLP); err != nil || requested.Hash() != request.BlockHash {
		return ImportResult{}, errors.New("invalid client execution block")
	}
	phases.parent, phases.transactions = requested.ParentHash(), len(requested.Transactions())
	if number := requested.Number64(); number != nil && number.IsUint64() {
		phases.number = number.Uint64()
	}
	phases.phase("request_json")
	raw, err := json.Marshal(request)
	if err != nil {
		return ImportResult{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/import", bytes.NewReader(raw))
	if err != nil {
		return ImportResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	phases.phase("http_roundtrip_headers")
	response, err := c.http.Do(r)
	if err != nil {
		return ImportResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return ImportResult{}, errors.New("execution service rejected import")
	}
	phases.phase("response_read")
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(responseBytes) > 16384 {
		return ImportResult{}, errors.New("invalid execution response size")
	}
	phases.phase("response_json")
	decoder := json.NewDecoder(bytes.NewReader(responseBytes))
	decoder.DisallowUnknownFields()
	var result ImportResult
	if err := decoder.Decode(&result); err != nil {
		return ImportResult{}, err
	}
	phases.phase("response_validate")
	if err := validateImportResult(c.identity, request.BlockHash, requested.Header().(*block.Header), result, response.StatusCode); err != nil {
		return ImportResult{}, err
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return ImportResult{}, errors.New("invalid trailing execution response")
	}
	return result, nil
}

// All full-body and reference responses use the same commitment checks. A
// transport optimization cannot relax the evidence used by import-gated votes.
func validateImportResult(identity Identity, hash types.Hash, header *block.Header, result ImportResult, status int) error {
	if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Hash() != hash {
		return errors.New("invalid expected execution header")
	}
	if result.Identity != identity || result.BlockHash != hash || result.ParentHash != header.ParentHash || result.Number != header.Number.Uint64() {
		return errors.New("execution response identity or block hash mismatch")
	}
	if result.Durable && (result.State != "durable" || !result.Checked || !result.Executed || status != http.StatusOK) {
		return errors.New("invalid durable execution response")
	}
	if result.Durable && (result.Result.Root != header.Root || result.Result.ReceiptHash != header.ReceiptHash || result.Result.Bloom != header.Bloom || result.Result.GasUsed != header.GasUsed) {
		return errors.New("execution response does not match block execution commitment")
	}
	if !result.Durable && (result.Checked || result.Executed || status != http.StatusAccepted || (result.State != "accepted" && result.State != "failed")) {
		return errors.New("invalid non-durable execution response")
	}
	return nil
}
