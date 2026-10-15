package sharedexec

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"io"
	"net/http"
)

// Commit requires an explicit owner capability; importing grants no authority
// to finalize blocks. The owner must decide finality before calling this RPC.
func (c *Client) Commit(ctx context.Context, request CommitRequest, owner [32]byte) (CommitResult, error) {
	if request.Identity != c.identity || request.Number == 0 || owner == ([32]byte{}) {
		return CommitResult{}, errors.New("explicit commit identity, height and owner capability required")
	}
	return c.commitRPC(ctx, "/v1/commit", request, request, owner)
}

func (c *Client) CommitCertified(ctx context.Context, request CommitRequest, qc *hotstuff.QuorumCertificate) (CommitResult, error) {
	if request.Identity != c.identity || request.Number == 0 || qc == nil || qc.BlockHash != request.BlockHash {
		return CommitResult{}, errors.New("explicit certified commit identity/height/certificate required")
	}
	return c.commitRPC(ctx, "/v1/commit-qc", CertifiedCommitRequest{CommitRequest: request, Certificate: qc.Clone()}, request, [32]byte{})
}

func (c *Client) commitRPC(ctx context.Context, path string, payload any, request CommitRequest, owner [32]byte) (CommitResult, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return CommitResult{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix"+path, bytes.NewReader(raw))
	if err != nil {
		return CommitResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	if owner != ([32]byte{}) {
		r.Header.Set("X-N42-Commit-Owner", hex.EncodeToString(owner[:]))
	}
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
