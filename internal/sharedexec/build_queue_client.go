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
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/crypto"
)

// BuildFromQueue validates a complete returned body and permits only its
// transaction root plus the existing execution-result additions to the leader
// header. The original full-template Build validator remains unchanged.
func (c *Client) BuildFromQueue(ctx context.Context, request QueueBuildRequest) (out QueueBuildResult, err error) {
	phases := newOwnerOperationPhases("client_build_queue", nil)
	phases.identity = c.identity
	phases.hash = request.PreparedHash
	defer func() { phases.finish(err) }()
	phases.phase("request_validate")
	if request.Identity != c.identity || request.Epoch == ([16]byte{}) || len(request.PreparedRLP) == 0 || len(request.PreparedRLP) > MaxQueuePreparedBytes || request.MaxTransactions < 1 || request.MaxTransactions > MaxTransactions || request.MaxEncodedBytes < 1 || request.MaxEncodedBytes > MaxQueueBuildEncodedBytes || crypto.Keccak256Hash(request.PreparedRLP) != request.PreparedHash {
		return QueueBuildResult{}, errors.New("invalid client queue build binding/limits")
	}
	var prepared block.Block
	if err := prepared.Unmarshal(request.PreparedRLP); err != nil {
		return QueueBuildResult{}, err
	}
	want, wantOK := prepared.Header().(*block.Header)
	body, bodyOK := prepared.Body().(*block.Body)
	if !wantOK || want == nil || want.Number == nil || !want.Number.IsUint64() || want.Number.IsZero() || !bodyOK || body == nil || body.IsFrameBody() || len(prepared.Transactions()) != 0 || len(body.Verifiers) > 0 || len(body.Rewards) > 0 || len(body.ZkProof) > 0 || want.TxHash != block.BodyTxRoot(body, want.Time) {
		return QueueBuildResult{}, errors.New("client queue build requires empty prepared body")
	}
	phases.parent, phases.number = want.ParentHash, want.Number.Uint64()
	phases.phase("request_json")
	raw, err := json.Marshal(request)
	if err != nil {
		return QueueBuildResult{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/build-queue", bytes.NewReader(raw))
	if err != nil {
		return QueueBuildResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	phases.phase("http_roundtrip_headers")
	response, err := c.http.Do(r)
	if err != nil {
		return QueueBuildResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return QueueBuildResult{}, fmt.Errorf("execution service rejected queue build: %s", bytes.TrimSpace(detail))
	}
	phases.phase("response_read")
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBytes+1))
	if err != nil || len(data) > maxRequestBytes {
		return QueueBuildResult{}, errors.New("invalid queue build response size")
	}
	phases.phase("response_json")
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result QueueBuildResult
	if err := decoder.Decode(&result); err != nil {
		return QueueBuildResult{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return QueueBuildResult{}, errors.New("trailing queue build response")
	}
	phases.phase("response_validate")
	if err := validateQueueBuildResponse(c.identity, request, &prepared, result); err != nil {
		return QueueBuildResult{}, err
	}
	phases.hash = result.BlockHash
	return result, nil
}

func validateQueueBuildResponse(identity Identity, request QueueBuildRequest, prepared *block.Block, result QueueBuildResult) error {
	if result.Identity != identity || result.Epoch != request.Epoch || result.PreparedHash != request.PreparedHash || len(result.BlockRLP) == 0 || len(result.BlockRLP) > MaxBlockBytes {
		return errors.New("queue build response binding/size mismatch")
	}
	var built block.Block
	if err := built.Unmarshal(result.BlockRLP); err != nil || built.Hash() != result.BlockHash {
		return errors.New("queue build response block mismatch")
	}
	got, ok := built.Header().(*block.Header)
	want, wantOK := prepared.Header().(*block.Header)
	body, bodyOK := built.Body().(*block.Body)
	if !ok || got == nil || !wantOK || want == nil || got.Number == nil || want.Number == nil || !got.Number.IsUint64() || !want.Number.IsUint64() || !bodyOK || body == nil || body.IsFrameBody() || len(body.Verifiers) > 0 || len(body.ZkProof) > 0 || len(built.Transactions()) > request.MaxTransactions {
		return errors.New("queue build response header/body/count mismatch")
	}
	if got.Root != result.Result.Root || got.ReceiptHash != result.Result.ReceiptHash || got.Bloom != result.Result.Bloom || got.GasUsed != result.Result.GasUsed || got.TxHash != block.BodyTxRoot(body, got.Time) {
		return errors.New("queue build response result/body commitments mismatch")
	}
	// Finalize may add rewards, but the queue template never carries verifier
	// or proof payloads. When scheduled, rewards have their own header commitment.
	if got.WithdrawalsHash != nil && *got.WithdrawalsHash != hash.DeriveSha(block.Rewards(body.Rewards)) {
		return errors.New("queue build response rewards commitment mismatch")
	}
	encoded := 0
	for _, txn := range built.Transactions() {
		wire, err := txn.EthEncoded()
		if err != nil {
			return err
		}
		encoded += 4 + len(wire)
		if encoded > request.MaxEncodedBytes {
			return errors.New("queue build response exceeds selected byte limit")
		}
	}
	selected := block.CopyHeader(got)
	selected.Root, selected.ReceiptHash, selected.Bloom, selected.GasUsed = want.Root, want.ReceiptHash, want.Bloom, want.GasUsed
	selected.WithdrawalsHash, selected.BlobGasUsed, selected.ExcessBlobGas, selected.RequestsHash = want.WithdrawalsHash, want.BlobGasUsed, want.ExcessBlobGas, want.RequestsHash
	selected.ResetHashCache()
	compare := block.CopyHeader(selected)
	compare.TxHash = want.TxHash
	compare.ResetHashCache()
	if compare.Hash() != want.Hash() {
		return errors.New("queue build response changed prepared leader header")
	}
	// Bind the selected full template, including exact raw transactions, to the
	// response's existing TemplateHash rather than trusting an arbitrary digest.
	template, err := block.NewBlock(selected, built.Transactions()).Marshal()
	if err != nil || crypto.Keccak256Hash(template) != result.TemplateHash {
		return errors.New("queue build response selected template hash mismatch")
	}
	return nil
}
