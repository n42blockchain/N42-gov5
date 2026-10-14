package sharedexec

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Commit requires an explicit owner capability; importing grants no authority
// to finalize blocks. The owner must decide finality before calling this RPC.
func (c *Client) Commit(ctx context.Context, request CommitRequest, owner [32]byte) (CommitResult, error) {
	if request.Identity != c.identity || request.Number == 0 || owner == ([32]byte{}) {
		return CommitResult{}, errors.New("explicit commit identity, height and owner capability required")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return CommitResult{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/commit", bytes.NewReader(raw))
	if err != nil {
		return CommitResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-N42-Commit-Owner", hex.EncodeToString(owner[:]))
	response, err := c.http.Do(r)
	if err != nil {
		return CommitResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return CommitResult{}, errors.New("execution service rejected commit")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(data) > 4096 {
		return CommitResult{}, errors.New("invalid commit response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result CommitResult
	if err := decoder.Decode(&result); err != nil {
		return CommitResult{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return CommitResult{}, errors.New("invalid trailing commit response")
	}
	if result.CommitRequest != request || !result.Committed || result.HeadNumber < request.Number || (result.HeadNumber == request.Number && result.HeadHash != request.BlockHash) {
		return CommitResult{}, errors.New("commit response identity or canonical head mismatch")
	}
	return result, nil
}
