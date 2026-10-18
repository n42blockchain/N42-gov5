package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

type AppliedChildRequest struct {
	Identity   Identity            `json:"identity"`
	ParentHash types.Hash          `json:"parent_hash"`
	Number     uint64              `json:"number"`
	View       hotstuff.ViewNumber `json:"view"`
}

type AppliedChildResult struct {
	AppliedChildRequest
	BlockRLP []byte        `json:"block_rlp,omitempty"`
	Executed *ImportResult `json:"executed,omitempty"`
}

// AppliedChild is read-only. It permits later leaders to re-propose an already
// sealed/executed payload without building a sibling on stale world state.
func (s *Server) AppliedChild(ctx context.Context, request AppliedChildRequest) (result AppliedChildResult, err error) {
	phases := newOwnerOperationPhases("applied_child", s)
	phases.parent, phases.number = request.ParentHash, request.Number
	defer func() { phases.finish(err) }()
	phases.phase("request_binding")
	result = AppliedChildResult{AppliedChildRequest: request}
	if request.Identity != s.identity || request.Number == 0 || request.View == 0 || request.ParentHash == (types.Hash{}) {
		return result, errors.New("invalid applied child identity/parent/height/view")
	}
	s.mu.Lock()
	if s.closed || s.resumeArming {
		s.mu.Unlock()
		return result, errors.New("execution service closed")
	}
	s.started = true
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	phases.phase("execute_wait")
	s.execute.Lock()
	defer s.execute.Unlock()
	phases.phase("request_context")
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := s.ctx.Err(); err != nil {
		return result, err
	}
	phases.phase("stored_child_read")
	child, err := s.chain.PerformanceAppliedChild(ctx, request.ParentHash, request.Number, request.View)
	if err != nil || child == nil {
		return result, err
	}
	phases.hash, phases.transactions = child.Hash(), len(child.Transactions())
	phases.phase("durable_evidence")
	executed, err := s.durable(child)
	if err != nil {
		return result, err
	}
	if executed == nil {
		return result, errors.New("applied child lacks durable execution evidence")
	}
	phases.phase("marshal")
	raw, err := child.Marshal()
	if err != nil || len(raw) > MaxBlockBytes {
		return result, errors.New("applied child encoding failed or exceeds limit")
	}
	result.BlockRLP, result.Executed = raw, executed
	return result, nil
}

func (s *Server) serveAppliedChild(w http.ResponseWriter, r *http.Request) {
	// This outer record includes framing serialization and JSON/base64 encoding.
	// Its nested applied_child record overlaps it; do not add the two totals.
	phases := newOwnerOperationPhases("applied_child_http", s)
	var operationErr error
	defer func() { phases.finish(operationErr) }()
	phases.phase("build_http_wait")
	// Large read-only payloads share Build's framing lock. Seven clients
	// cannot create seven simultaneous owner-side base64 response buffers.
	s.buildHTTP.Lock()
	defer s.buildHTTP.Unlock()
	defer r.Body.Close()
	phases.phase("request_decode")
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request AppliedChildRequest
	if operationErr = decoder.Decode(&request); operationErr != nil {
		http.Error(w, "invalid applied child request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		operationErr = errors.New("one applied child request required")
		http.Error(w, operationErr.Error(), http.StatusBadRequest)
		return
	}
	phases.parent, phases.number = request.ParentHash, request.Number
	phases.phase("applied_child")
	result, err := s.AppliedChild(r.Context(), request)
	if err != nil {
		operationErr = err
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if result.Executed != nil {
		phases.hash = result.Executed.BlockHash
	}
	phases.phase("response_encode")
	w.Header().Set("Content-Type", "application/json")
	operationErr = json.NewEncoder(w).Encode(result)
}

func (c *Client) AppliedChild(ctx context.Context, request AppliedChildRequest) (AppliedChildResult, error) {
	if request.Identity != c.identity || request.Number == 0 || request.View == 0 || request.ParentHash == (types.Hash{}) {
		return AppliedChildResult{}, errors.New("invalid client applied child request")
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return AppliedChildResult{}, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://unix/v1/applied-child", bytes.NewReader(raw))
	if err != nil {
		return AppliedChildResult{}, err
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(r)
	if err != nil {
		return AppliedChildResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return AppliedChildResult{}, errors.New("execution service rejected applied child query")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxRequestBytes+16385))
	if err != nil || len(data) > maxRequestBytes+16384 {
		return AppliedChildResult{}, errors.New("applied child response oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result AppliedChildResult
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF || result.AppliedChildRequest != request {
		return result, errors.New("applied child response framing/identity mismatch")
	}
	if len(result.BlockRLP) == 0 {
		if result.Executed != nil {
			return result, errors.New("applied child evidence without block")
		}
		return result, nil
	}
	if len(result.BlockRLP) > MaxBlockBytes || result.Executed == nil {
		return result, errors.New("applied child block size/evidence invalid")
	}
	var b block.Block
	if err := b.Unmarshal(result.BlockRLP); err != nil || b.Number64() == nil || !b.Number64().IsUint64() || b.Number64().Uint64() != request.Number || b.ParentHash() != request.ParentHash {
		return result, errors.New("applied child block binding mismatch")
	}
	header := b.Header().(*block.Header)
	body, ok := b.Body().(*block.Body)
	if !ok || body == nil || body.IsFrameBody() || len(b.Transactions()) > MaxTransactions || b.TxHash() != block.BodyTxRoot(body, header.Time) {
		return result, errors.New("applied child body commitment mismatch")
	}
	view, err := hotstuff.ExtractViewFromExtra(header.Extra)
	e := result.Executed
	if err != nil || hotstuff.ViewNumber(view) > request.View || e.Identity != c.identity || e.BlockHash != b.Hash() || e.ParentHash != request.ParentHash || e.Number != request.Number || !e.Durable || !e.Checked || !e.Executed || e.State != "durable" || e.Error != "" || e.Result.Root != header.Root || e.Result.ReceiptHash != header.ReceiptHash || e.Result.GasUsed != header.GasUsed || e.Result.Bloom != header.Bloom {
		return result, errors.New("applied child execution or view binding mismatch")
	}
	return result, nil
}
