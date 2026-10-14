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

func (c *Client) Import(ctx context.Context, request ImportRequest) (ImportResult, error) {
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
	raw, err := json.Marshal(request)
	if err != nil {
		return ImportResult{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/import", bytes.NewReader(raw))
	if err != nil {
		return ImportResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return ImportResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return ImportResult{}, errors.New("execution service rejected import")
	}
	responseBytes, err := io.ReadAll(io.LimitReader(response.Body, 16385))
	if err != nil || len(responseBytes) > 16384 {
		return ImportResult{}, errors.New("invalid execution response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBytes))
	decoder.DisallowUnknownFields()
	var result ImportResult
	if err := decoder.Decode(&result); err != nil {
		return ImportResult{}, err
	}
	if result.Identity != c.identity || result.BlockHash != request.BlockHash || result.ParentHash != requested.ParentHash() || result.Number != requested.Number64().Uint64() {
		return ImportResult{}, errors.New("execution response identity or block hash mismatch")
	}
	if result.Durable && (result.State != "durable" || !result.Checked || !result.Executed || response.StatusCode != http.StatusOK) {
		return ImportResult{}, errors.New("invalid durable execution response")
	}
	if result.Durable {
		header := requested.Header().(*block.Header)
		if result.Result.Root != header.Root || result.Result.ReceiptHash != header.ReceiptHash || result.Result.Bloom != header.Bloom || result.Result.GasUsed != header.GasUsed {
			return ImportResult{}, errors.New("execution response does not match block execution commitment")
		}
	}
	if !result.Durable && (result.Checked || result.Executed || response.StatusCode != http.StatusAccepted || (result.State != "accepted" && result.State != "failed")) {
		return ImportResult{}, errors.New("invalid non-durable execution response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return ImportResult{}, errors.New("invalid trailing execution response")
	}
	return result, nil
}
