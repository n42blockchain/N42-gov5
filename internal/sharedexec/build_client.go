package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
)

func (c *Client) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if request.Identity != c.identity || len(request.TemplateRLP) == 0 || len(request.TemplateRLP) > MaxBlockBytes || crypto.Keccak256Hash(request.TemplateRLP) != request.TemplateHash {
		return BuildResult{}, errors.New("invalid client candidate identity/hash/size")
	}
	var template block.Block
	if err := template.Unmarshal(request.TemplateRLP); err != nil {
		return BuildResult{}, err
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return BuildResult{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/build", bytes.NewReader(raw))
	if err != nil {
		return BuildResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return BuildResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return BuildResult{}, fmt.Errorf("execution service rejected candidate build: %s", bytes.TrimSpace(detail))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBytes+1))
	if err != nil || len(data) > maxRequestBytes {
		return BuildResult{}, errors.New("invalid build response size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result BuildResult
	if err := decoder.Decode(&result); err != nil {
		return BuildResult{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return BuildResult{}, errors.New("invalid trailing build response")
	}
	if result.Identity != c.identity || result.TemplateHash != request.TemplateHash || len(result.BlockRLP) == 0 || len(result.BlockRLP) > MaxBlockBytes {
		return BuildResult{}, errors.New("build response identity/hash/size mismatch")
	}
	var built block.Block
	if err := built.Unmarshal(result.BlockRLP); err != nil || built.Hash() != result.BlockHash {
		return BuildResult{}, errors.New("build response block mismatch")
	}
	got, ok := built.Header().(*block.Header)
	want, wantOK := template.Header().(*block.Header)
	if !ok || !wantOK || got.Number == nil || want.Number == nil || !got.Number.IsUint64() || !want.Number.IsUint64() {
		return BuildResult{}, errors.New("build response missing header")
	}
	if got.Root != result.Result.Root || got.ReceiptHash != result.Result.ReceiptHash || got.Bloom != result.Result.Bloom || got.GasUsed != result.Result.GasUsed || built.TxHash() != template.TxHash() {
		return BuildResult{}, errors.New("build response execution/body commitments mismatch")
	}
	// Only execution and fork-default result fields may be added. In
	// particular, proposer/view/QC/zero seal, parent, timestamp and gas limit
	// must remain the leader's prepared values.
	compare := block.CopyHeader(got)
	compare.Root, compare.ReceiptHash, compare.Bloom, compare.GasUsed = want.Root, want.ReceiptHash, want.Bloom, want.GasUsed
	compare.WithdrawalsHash, compare.BlobGasUsed, compare.ExcessBlobGas, compare.RequestsHash = want.WithdrawalsHash, want.BlobGasUsed, want.ExcessBlobGas, want.RequestsHash
	compare.ResetHashCache()
	if compare.Hash() != want.Hash() {
		return BuildResult{}, errors.New("build response changed prepared leader header")
	}
	return result, nil
}
