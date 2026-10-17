// Package sharedexec exposes one actual Go BlockChain executor over a private
// Unix socket. It is a trusted execution source, not a consensus validity proof.
package sharedexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

const MaxBlockBytes = 64 << 20
const MaxTransactions = 200000
const maxRequestBytes = MaxBlockBytes*4/3 + 4096

type Identity struct {
	Version     uint64     `json:"version"`
	Namespace   string     `json:"namespace"`
	ChainID     string     `json:"chain_id"`
	GenesisHash types.Hash `json:"genesis_hash"`
}

type ImportRequest struct {
	Identity  Identity   `json:"identity"`
	BlockHash types.Hash `json:"block_hash"`
	BlockRLP  []byte     `json:"block_rlp"`
}

type ImportResult struct {
	Identity   Identity             `json:"identity"`
	BlockHash  types.Hash           `json:"block_hash"`
	ParentHash types.Hash           `json:"parent_hash"`
	Number     uint64               `json:"number"`
	State      string               `json:"state"`
	Checked    bool                 `json:"checked"`
	Executed   bool                 `json:"executed"`
	Durable    bool                 `json:"durable"`
	Result     rawdb.ExecutedResult `json:"result"`
	Error      string               `json:"error,omitempty"`
}

type importKey struct {
	identity Identity
	hash     types.Hash
}
type importJob struct {
	done   chan struct{}
	result ImportResult
}

type Server struct {
	chain            *internal.BlockChain
	identity         Identity
	capacity         int
	ctx              context.Context
	cancel           context.CancelFunc
	mu               sync.Mutex
	jobs             map[importKey]*importJob
	completed        []importKey
	wg               sync.WaitGroup
	imports          atomic.Uint64
	closed           bool
	started          bool // mu: external or direct execution use has begun
	resumeArming     bool // mu: exclusive long-running startup verification
	execute          sync.Mutex
	commitOwner      [32]byte
	builds           map[importKey]*buildJob
	buildCompleted   []importKey
	buildBytes       int
	buildCalls       atomic.Uint64
	buildHTTP        sync.Mutex
	reuseOwnBuild    bool
	prepared         *internal.PreparedUnsignedCandidate // execute lock; one pending candidate
	ownBuildReuses   atomic.Uint64
	ingress          *strictIngressQueue                     // configured before serving; immutable pointer
	queuePlans       map[hotstuff.ViewNumber]*queueBuildPlan // execute; registry size/accounting also mu
	queueExpiredView hotstuff.ViewNumber                     // execute
}

// New requires one explicitly configured performance chain. Deferred and frame
// execution need staged result/hook transport and remain fail-closed here.
func New(chain *internal.BlockChain, capacity int) (*Server, error) {
	if chain == nil || chain.Config() == nil || chain.Config().Performance == nil || capacity < 1 || capacity > 256 {
		return nil, errors.New("shared executor requires a performance BlockChain and capacity 1..256")
	}
	cfg := chain.Config()
	profile, _ := params.ResolveExecutionProfile("n42")
	if err := cfg.ValidatePerformanceSelection("private", profile); err != nil {
		return nil, err
	}
	if cfg.DeferredExecutionTime != nil || block.DeferredExecutionTime != 0 {
		return nil, errors.New("shared executor deferred results are not wired yet")
	}
	if chain.GenesisBlock() == nil || chain.GenesisBlock().Hash() == (types.Hash{}) {
		return nil, errors.New("shared executor requires actual genesis identity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{chain: chain, identity: Identity{Version: 1, Namespace: cfg.Performance.NetworkNamespace, ChainID: cfg.ChainID.String(), GenesisHash: chain.GenesisBlock().Hash()},
		capacity: capacity, ctx: ctx, cancel: cancel, jobs: make(map[importKey]*importJob)}, nil
}

func (s *Server) Identity() Identity  { return s.identity }
func (s *Server) ImportCalls() uint64 { return s.imports.Load() }

// Close prevents new jobs and drains active imports. A canceled requester never
// deletes ownership while its BlockChain import is still running.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.mu.Unlock()
	s.wg.Wait()
	s.execute.Lock()
	s.prepared = nil
	if q := s.ingress; q != nil {
		q.mu.Lock()
		q.closed = true
		q.lanes = nil
		q.pending = 0
		q.bytes = 0
		q.mu.Unlock()
	}
	s.execute.Unlock()
	s.mu.Lock()
	s.builds = nil
	s.queuePlans = nil
	s.buildCompleted = nil
	s.buildBytes = 0
	s.mu.Unlock()
}

func (s *Server) Import(ctx context.Context, request ImportRequest) (ImportResult, error) {
	if request.Identity != s.identity {
		return ImportResult{}, errors.New("execution chain identity mismatch")
	}
	if len(request.BlockRLP) == 0 || len(request.BlockRLP) > MaxBlockBytes {
		return ImportResult{}, errors.New("invalid execution block size")
	}
	var b block.Block
	if err := b.Unmarshal(request.BlockRLP); err != nil {
		return ImportResult{}, fmt.Errorf("decode execution block: %w", err)
	}
	header, ok := b.Header().(*block.Header)
	body, bodyOK := b.Body().(*block.Body)
	if !ok || !bodyOK || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() || b.Hash() != request.BlockHash {
		return ImportResult{}, errors.New("execution block hash or number mismatch")
	}
	if header.Root == (types.Hash{}) || header.ReceiptHash == (types.Hash{}) {
		return ImportResult{}, errors.New("import requires nonzero execution commitments; unsigned templates use Build")
	}
	if body.IsFrameBody() {
		return ImportResult{}, errors.New("execution frame transport is not wired yet")
	}
	if len(b.Transactions()) > MaxTransactions {
		return ImportResult{}, errors.New("execution transaction limit exceeded")
	}
	if b.TxHash() != block.BodyTxRoot(body, header.Time) {
		return ImportResult{}, errors.New("execution body transaction root mismatch")
	}
	key := importKey{s.identity, b.Hash()}
	s.mu.Lock()
	if s.closed || s.resumeArming {
		s.mu.Unlock()
		return ImportResult{}, errors.New("execution service closed")
	}
	s.started = true
	job, found := s.jobs[key]
	if !found {
		for len(s.jobs) >= s.capacity && len(s.completed) > 0 {
			oldest := s.completed[0]
			s.completed = s.completed[1:]
			delete(s.jobs, oldest)
		}
		if len(s.jobs) >= s.capacity {
			s.mu.Unlock()
			return ImportResult{}, errors.New("execution capacity busy; retry")
		}
		job = &importJob{done: make(chan struct{})}
		s.jobs[key] = job
		s.wg.Add(1)
		go s.run(key, job, &b)
	}
	s.mu.Unlock()
	select {
	case <-ctx.Done():
		return ImportResult{}, ctx.Err()
	case <-job.done:
		return job.result, nil
	}
}

func (s *Server) run(key importKey, job *importJob, b *block.Block) {
	defer s.wg.Done()
	result := ImportResult{Identity: s.identity, BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64(), State: "accepted"}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.State = "failed"
			result.Error = "execution worker failed"
		}
		s.mu.Lock()
		job.result = result
		if result.Durable {
			s.completed = append(s.completed, key)
		} else {
			delete(s.jobs, key)
		}
		close(job.done)
		s.mu.Unlock()
	}()
	s.execute.Lock()
	defer s.execute.Unlock()
	if s.ctx.Err() != nil {
		result.Error = "execution service stopping; retry"
		return
	}
	if durable, err := s.durable(b); err != nil {
		result.State = "failed"
		result.Error = err.Error()
		return
	} else if durable != nil {
		result = *durable
		return
	}
	if !s.chain.HasAppliedBlock(b.ParentHash(), result.Number-1) {
		result.Error = "parent is not applied; retry"
		return
	}
	prepared := s.prepared
	s.prepared = nil
	reused := false
	var err error
	if prepared != nil {
		reused, err = s.chain.ImportPreparedCandidate(s.ctx, b, prepared)
	}
	if err == nil && !reused {
		_, err = s.chain.InsertPerformanceExtension(s.ctx, b)
	}
	if reused {
		s.ownBuildReuses.Add(1)
	}
	if errors.Is(err, internal.ErrPerformanceParentNotHead) {
		result.Error = err.Error()
		return
	}
	s.imports.Add(1)
	if err != nil {
		result.State = "failed"
		result.Error = err.Error()
		return
	}
	durable, err := s.durable(b)
	if err != nil {
		result.State = "failed"
		result.Error = err.Error()
		return
	}
	if durable == nil {
		result.Error = "import accepted without durable execution; retry"
		return
	}
	result = *durable
}

func (s *Server) durable(b *block.Block) (*ImportResult, error) {
	if !s.chain.HasAppliedBlock(b.Hash(), b.Number64().Uint64()) {
		return nil, nil
	}
	var result *ImportResult
	err := s.chain.DB().View(s.ctx, func(tx kv.Tx) error {
		_, _, marker, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !marker {
			return err
		}
		header := rawdb.ReadHeader(tx, b.Hash(), b.Number64().Uint64())
		if header == nil {
			return nil
		}
		receipts, err := rawdb.ReadReceiptsByHash(tx, b.Hash())
		if err != nil {
			return err
		}
		if len(receipts) != len(b.Transactions()) {
			return errors.New("execution receipt count mismatch")
		}
		executed := internal.ExecutedResultFor(s.chain.Config(), b.Number64().Uint64(), header.Root, receipts)
		if executed.ReceiptHash != header.ReceiptHash || executed.GasUsed != header.GasUsed || executed.Bloom != header.Bloom {
			return errors.New("durable execution result does not match stored header")
		}
		result = &ImportResult{Identity: s.identity, BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64(), State: "durable", Checked: true, Executed: true, Durable: true, Result: executed}
		return nil
	})
	return result, err
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.closed || s.resumeArming {
		s.mu.Unlock()
		http.Error(w, "execution owner closed or initializing resume", http.StatusServiceUnavailable)
		return
	}
	s.started = true
	s.mu.Unlock()

	if r.Method == http.MethodPost && r.URL.Path == "/v1/import-ref" {
		s.serveImportRef(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/ingress-accounts" {
		s.serveCanonicalAccounts(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/ingress-status" {
		s.serveIngressStatus(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/build-queue" {
		s.serveQueueBuild(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/applied-child" {
		s.serveAppliedChild(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/commit-qc" {
		s.serveCertifiedCommit(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/commit" {
		s.serveCommit(w, r)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/build" {
		s.serveBuild(w, r)
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/import" {
		http.NotFound(w, r)
		return
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request ImportRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid execution request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one execution request required", http.StatusBadRequest)
		return
	}
	result, err := s.Import(r.Context(), request)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if !result.Durable {
		w.WriteHeader(http.StatusAccepted)
	}
	_ = json.NewEncoder(w).Encode(result)
}

// ListenUnix refuses symlinked/public directories and existing socket paths.
// Filesystem permissions authorize local clients; no public TCP listener exists.
func (s *Server) ListenUnix(path string) (*http.Server, net.Listener, error) {
	s.mu.Lock()
	if s.closed || s.resumeArming {
		s.mu.Unlock()
		return nil, nil, errors.New("execution owner closed or initializing resume")
	}
	s.started = true
	s.mu.Unlock()

	listener, err := listenPrivateExecutionUnix(path)
	if err != nil {
		return nil, nil, err
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4096}
	return server, listener, nil
}

func listenPrivateExecutionUnix(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("execution socket path must be absolute")
	}
	dir := filepath.Dir(path)
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real != dir {
		return nil, errors.New("execution socket directory must exist without symlinks")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("execution socket directory requires private permissions")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil, errors.New("execution socket path already exists")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}
