package sharedexec

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/modules/rawdb"
)

const MaxBuildRetainedBytes = 128 << 20

type BuildRequest struct {
	Identity     Identity   `json:"identity"`
	TemplateHash types.Hash `json:"template_hash"`
	TemplateRLP  []byte     `json:"template_rlp"`
}

type BuildResult struct {
	Identity     Identity             `json:"identity"`
	TemplateHash types.Hash           `json:"template_hash"`
	BlockHash    types.Hash           `json:"block_hash"`
	BlockRLP     []byte               `json:"block_rlp"`
	Result       rawdb.ExecutedResult `json:"result"`
}

type buildJob struct {
	done   chan struct{}
	result BuildResult
	err    error
	bytes  int
}

func (s *Server) BuildCalls() uint64 { return s.buildCalls.Load() }

// Build keys ownership on the complete template, including view, proposer, QC
// and transactions. Only one worker decodes/executes a candidate at a time.
// Registry bytes include owned input bytes and reserved output, then the actual
// output length alone when complete. No decoded candidate is kept in the cache.
func (s *Server) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if request.Identity != s.identity || len(request.TemplateRLP) == 0 || len(request.TemplateRLP) > MaxBlockBytes || crypto.Keccak256Hash(request.TemplateRLP) != request.TemplateHash {
		return BuildResult{}, errors.New("build template size, hash or chain identity mismatch")
	}
	key := importKey{s.identity, request.TemplateHash}
	s.mu.Lock()
	if s.closed || s.resumeArming {
		s.mu.Unlock()
		return BuildResult{}, errors.New("execution service closed")
	}
	s.started = true
	if s.builds == nil {
		s.builds = make(map[importKey]*buildJob)
	}
	job, found := s.builds[key]
	if !found {
		// Outputs have the same body plus result header/rewards. Reserve an
		// extra header allowance; oversized results are rejected, not cached.
		reserved := 2*len(request.TemplateRLP) + 4096
		for (len(s.builds)+len(s.queuePlans) >= s.capacity || s.buildBytes+reserved > MaxBuildRetainedBytes) && len(s.buildCompleted) > 0 {
			old := s.buildCompleted[0]
			s.buildCompleted = s.buildCompleted[1:]
			s.buildBytes -= s.builds[old].bytes
			delete(s.builds, old)
		}
		if len(s.builds)+len(s.queuePlans) >= s.capacity || s.buildBytes+reserved > MaxBuildRetainedBytes {
			s.mu.Unlock()
			return BuildResult{}, errors.New("build capacity or retained payload budget busy; retry")
		}
		job = &buildJob{done: make(chan struct{}), bytes: reserved}
		input := append([]byte(nil), request.TemplateRLP...)
		s.builds[key] = job
		s.buildBytes += reserved
		s.wg.Add(1)
		go s.runBuild(key, job, input)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return BuildResult{}, ctx.Err()
	case <-job.done:
		result := job.result
		result.BlockRLP = append([]byte(nil), result.BlockRLP...)
		return result, job.err
	}
}

func (s *Server) runBuild(key importKey, job *buildJob, input []byte) {
	defer s.wg.Done()
	var result BuildResult
	var err error
	defer func() {
		if recover() != nil {
			err = errors.New("candidate build worker failed")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.buildBytes -= job.bytes
		job.result, job.err = result, err
		if err == nil && !s.closed {
			job.bytes = len(result.BlockRLP)
			s.buildBytes += job.bytes
			s.buildCompleted = append(s.buildCompleted, key)
		} else {
			delete(s.builds, key)
			job.bytes = 0
		}
		close(job.done)
	}()
	s.execute.Lock()
	defer s.execute.Unlock()
	result, err = s.executeBuildLocked(input, key.hash, job.bytes)
}

// executeBuildLocked requires execute, and never reacquires it. Callers retain
// their owned immutable input and publish accounting before releasing execute.
// This nested record overlaps build_queue/runBuild; do not add their totals.
func (s *Server) executeBuildLocked(input []byte, templateHash types.Hash, reserved int) (response BuildResult, err error) {
	return s.executeBuildWithIngressLocked(input, templateHash, reserved, nil)
}

func (s *Server) executeBuildWithIngressLocked(input []byte, templateHash types.Hash, reserved int, epoch *[16]byte) (response BuildResult, err error) {
	return s.executeBuildTemplateWithIngressLocked(input, templateHash, reserved, epoch, nil)
}

// executeBuildTemplateWithIngressLocked may reuse a queue-selected block only
// for its first, synchronous attempt. It never retains the mutable block;
// retries and public Build reconstruct from the immutable authenticated bytes.
func (s *Server) executeBuildTemplateWithIngressLocked(input []byte, templateHash types.Hash, reserved int, epoch *[16]byte, freshTemplate *block.Block) (response BuildResult, err error) {
	phases := newOwnerOperationPhases("execute_build", s)
	phases.hash = templateHash
	defer func() { phases.finish(err) }()
	phases.phase("input_hash_decode")
	if err := s.ctx.Err(); err != nil {
		return BuildResult{}, err
	}
	if crypto.Keccak256Hash(input) != templateHash {
		return BuildResult{}, errors.New("owned candidate template hash mismatch")
	}
	var decoded block.Block
	template := freshTemplate
	if template == nil {
		if err := decoded.Unmarshal(input); err != nil {
			return BuildResult{}, err
		}
		template = &decoded
	} else if epoch == nil {
		return BuildResult{}, errors.New("fresh queue template requires ingress epoch")
	}
	phases.phase("template_validate_root")
	header, ok := template.Header().(*block.Header)
	body, bodyOK := template.Body().(*block.Body)
	if !ok || header == nil || !bodyOK || body == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() || body.IsFrameBody() || len(template.Transactions()) > MaxTransactions || (freshTemplate == nil && template.TxHash() != block.BodyTxRoot(body, header.Time)) {
		return BuildResult{}, errors.New("invalid candidate template header or body")
	}
	phases.parent, phases.number, phases.transactions = header.ParentHash, header.Number.Uint64(), len(template.Transactions())
	if epoch != nil {
		phases.phase("ingress_sender_memos")
		phases.verifiedSenderMemos = s.seedLiveIngressSenderMemos(template, epoch)
	}
	s.buildCalls.Add(1)
	s.prepared = nil // a new build supersedes the only retained execution write
	var built *block.Block
	var executed rawdb.ExecutedResult
	var prepared *internal.PreparedUnsignedCandidate
	var buildErr error
	phases.phase("chain_build")
	if s.reuseOwnBuild {
		built, executed, prepared, buildErr = s.chain.BuildPreparedUnsignedCandidate(s.ctx, template)
	} else {
		built, executed, buildErr = s.chain.BuildUnsignedCandidate(s.ctx, template)
	}
	if buildErr != nil {
		return BuildResult{}, buildErr
	}
	phases.phase("result_marshal_copy")
	var raw []byte
	if prepared != nil {
		// Preparation already encoded this unchanged unsigned candidate. Keep
		// the response independently owned from the import authentication bytes.
		raw = prepared.CopyUnsignedRLP()
	} else {
		var marshalErr error
		raw, marshalErr = built.Marshal()
		if marshalErr != nil {
			return BuildResult{}, marshalErr
		}
	}
	if len(raw) > MaxBlockBytes || len(raw)+len(input) > reserved {
		return BuildResult{}, errors.New("candidate exceeds reserved payload budget")
	}
	s.prepared = prepared
	builtHash := built.Hash()
	phases.hash = builtHash
	return BuildResult{Identity: s.identity, TemplateHash: templateHash, BlockHash: builtHash, BlockRLP: raw, Result: executed}, nil
}

func (s *Server) serveBuild(w http.ResponseWriter, r *http.Request) {
	// Serialize JSON decoding/response framing as well as execution. This
	// avoids seven full base64 payload copies being decoded concurrently.
	// Waiters use the complete template key; hash-only waiting is a later RPC.
	s.buildHTTP.Lock()
	defer s.buildHTTP.Unlock()
	defer r.Body.Close()
	if r.Context().Err() != nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request BuildRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid build request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one build request required", http.StatusBadRequest)
		return
	}
	result, err := s.Build(r.Context(), request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}
