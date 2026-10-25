package sharedexec

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

// This is deliberately an advisory slot. It reserves neither queue credits
// nor a view plan and cannot produce an Import or Commit acknowledgement.
const maxSpeculativeQueueSnapshotBytes = 64 << 20
const maxSpeculativeQueueSnapshotEntries = 2 * MaxTransactions

// EnableSpeculativeQueue is a typed, startup-only opt-in. The standalone
// owner rejects inherited N42_* environment variables, so this must be
// selected explicitly by its CLI after strict ingress is configured.
func (s *Server) EnableSpeculativeQueue() error {
	s.execute.Lock()
	defer s.execute.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.started || s.ingress == nil {
		return errors.New("speculative queue requires unused strict-ingress owner")
	}
	s.speculativeEnabled = true
	return nil
}

type speculativeCommitTransition struct {
	from, to ingressStamp
	epoch    [16]byte
}

type speculativeQueueSlot struct {
	mu       sync.Mutex
	done     chan struct{}
	cleanup  chan struct{} // closed after child.Close and specDraining retirement
	cancel   context.CancelFunc
	timer    *time.Timer
	child    *internal.SpeculativeAppliedChild
	err      error
	epoch    [16]byte
	stamp    ingressStamp
	parent   types.Hash
	number   uint64
	view     hotstuff.ViewNumber
	maxTx    int
	maxBytes int
	binding  types.Hash
}

// DrainSpeculativeQueueRequest binds the irreversible recovery barrier to the
// strict-ingress owner session. A matching drain is idempotent.
type DrainSpeculativeQueueRequest struct {
	Identity Identity `json:"identity"`
	Epoch    [16]byte `json:"epoch"`
}

func (s *Server) serveDrainSpeculativeQueue(w http.ResponseWriter, r *http.Request) {
	if !s.speculativeEnabled {
		http.Error(w, "speculative queue disabled", http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request DrainSpeculativeQueueRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid speculative drain request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF || request.Identity != s.identity || request.Epoch == ([16]byte{}) {
		http.Error(w, "invalid speculative drain binding", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	q := s.ingress
	closed := s.closed
	s.mu.Unlock()
	if closed || q == nil {
		http.Error(w, "speculative owner unavailable", http.StatusServiceUnavailable)
		return
	}
	q.mu.Lock()
	current := q.epoch == request.Epoch && !q.closed
	q.mu.Unlock()
	if !current {
		http.Error(w, "speculative drain epoch changed", http.StatusConflict)
		return
	}
	// This lock also gates the final slot installation. A request that is
	// copying slabs may finish later, but can no longer install a worker.
	s.specMu.Lock()
	s.specRecoveryEpoch = request.Epoch
	s.specMu.Unlock()
	s.discardSpeculativeQueueSlot()
	// A concurrent drain can own specDraining; discard joins it. A pending
	// request cannot install after the tombstone and therefore needs no wait.
	s.specMu.Lock()
	drained := s.specSlot == nil && s.specDraining == nil && s.specRecoveryEpoch == request.Epoch
	s.specMu.Unlock()
	if !drained {
		http.Error(w, "speculative drain incomplete", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) discardSpeculativeQueueSlot() {
	s.specMu.Lock()
	slot := s.specSlot
	owner := slot != nil
	if owner {
		s.specSlot = nil
		s.specDraining = slot
	} else {
		slot = s.specDraining
	}
	s.specMu.Unlock()
	if slot != nil {
		if !owner {
			if slot.cleanup != nil {
				<-slot.cleanup
			} else {
				<-slot.done // synthetic test slots have no cleanup channel
			}
			return
		}
		s.stopSpeculativeQueueTimer(slot)
		slot.cancel()
		<-slot.done // BuildParallel may ignore cancellation until its fold ends.
		slot.mu.Lock()
		if slot.child != nil {
			slot.child.Close()
			slot.child = nil
		}
		slot.mu.Unlock()
		s.specMu.Lock()
		if s.specDraining == slot {
			s.specDraining = nil
		}
		s.specMu.Unlock()
		if slot.cleanup != nil {
			close(slot.cleanup)
		}
	}
}

func (s *Server) stopSpeculativeQueueTimer(slot *speculativeQueueSlot) {
	if slot.timer != nil && slot.timer.Stop() {
		s.specTimerWG.Done()
	}
}

func (s *Server) expireSpeculativeQueueSlot(slot *speculativeQueueSlot) {
	s.execute.Lock()
	defer s.execute.Unlock()
	s.beginSpeculativeExecutionExclusion()
	defer s.endSpeculativeExecutionExclusion()
	s.specMu.Lock()
	if s.specSlot != slot {
		s.specMu.Unlock()
		return
	}
	s.specSlot = nil
	s.specDraining = slot
	s.specMu.Unlock()
	slot.cancel()
	<-slot.done
	slot.mu.Lock()
	if slot.child != nil {
		slot.child.Close()
		slot.child = nil
	}
	slot.mu.Unlock()
	s.specMu.Lock()
	if s.specDraining == slot {
		s.specDraining = nil
	}
	s.specMu.Unlock()
	if slot.cleanup != nil {
		close(slot.cleanup)
	}
}

// execute must be held. This closes the install race between an ordinary
// executor draining the previous slot and an advisory HTTP request arriving.
func (s *Server) beginSpeculativeExecutionExclusion() {
	s.specMu.Lock()
	s.specExecutionBlocks++
	s.specMu.Unlock()
}

func (s *Server) endSpeculativeExecutionExclusion() {
	s.specMu.Lock()
	s.specExecutionBlocks--
	s.specMu.Unlock()
}

// execute serializes ordinary owner work, but the advisory EVM worker does
// not take execute. Every nonmatching executor must drain it before touching
// the chain's shared StateProcessor. Only the certified writer of its exact
// parent is allowed to overlap that worker.
func (s *Server) stopSpeculativeQueueUnlessCertifiedParent(certificate *hotstuff.QuorumCertificate, request CommitRequest) {
	if !s.speculativeEnabled {
		return
	}
	s.specMu.Lock()
	slot := s.specSlot
	matching := slot != nil && certificate != nil && slot.parent == request.BlockHash && slot.number == request.Number+1
	s.specMu.Unlock()
	if !matching {
		s.discardSpeculativeQueueSlot()
	}
}

func (s *Server) serveSpeculativeQueueBuild(w http.ResponseWriter, r *http.Request) {
	if !s.speculativeEnabled {
		http.Error(w, "speculative queue disabled", http.StatusNotFound)
		return
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 2*MaxQueuePreparedBytes+4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request QueueBuildRequest
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid speculative queue request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "one speculative queue request required", http.StatusBadRequest)
		return
	}
	if err := s.prepareSpeculativeQueueBuild(request); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) prepareSpeculativeQueueBuild(r QueueBuildRequest) error {
	if r.Identity != s.identity || r.MaxTransactions < 1 || r.MaxTransactions > MaxTransactions || r.MaxEncodedBytes < 1 || r.MaxEncodedBytes > MaxQueueBuildEncodedBytes || len(r.PreparedRLP) == 0 || len(r.PreparedRLP) > MaxQueuePreparedBytes || crypto.Keccak256Hash(r.PreparedRLP) != r.PreparedHash {
		return errors.New("speculative queue identity, bounds or prepared hash mismatch")
	}
	var prepared block.Block
	if err := prepared.Unmarshal(r.PreparedRLP); err != nil {
		return err
	}
	header, ok := prepared.Header().(*block.Header)
	body, bodyOK := prepared.Body().(*block.Body)
	if !ok || header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.IsZero() || !bodyOK || body == nil || len(body.Transactions()) != 0 || body.IsFrameBody() || len(body.Verifiers) != 0 || len(body.Rewards) != 0 || len(body.ZkProof) != 0 || header.TxHash != block.BodyTxRoot(body, header.Time) {
		return errors.New("speculative queue requires empty prepared body")
	}
	engine, ok := s.chain.Engine().(*hotstuff.HotStuff)
	if !ok {
		return errors.New("speculative queue public HotStuff verifier missing")
	}
	if err := engine.ValidateUnsignedCandidateHeader(s.chain, header); err != nil {
		return err
	}
	view, err := hotstuff.ExtractViewFromExtra(header.Extra)
	if err != nil {
		return err
	}
	// Reserve the one advisory allocation before copying any retained queue
	// slabs. Parallel HTTP hints cannot each allocate the 64 MiB bound.
	s.specMu.Lock()
	if s.ctx.Err() != nil || s.specRecoveryEpoch == r.Epoch || s.specSlot != nil || s.specDraining != nil || s.specPending || s.specExecutionBlocks != 0 {
		s.specMu.Unlock()
		return errors.New("speculative queue slot or executor busy")
	}
	s.specPending = true
	s.specMu.Unlock()
	defer func() {
		s.specMu.Lock()
		s.specPending = false
		s.specMu.Unlock()
	}()
	s.mu.Lock()
	q := s.ingress
	closed := s.closed || s.resumeArming
	s.mu.Unlock()
	if closed || q == nil || !s.speculativeEnabled {
		return errors.New("speculative queue unavailable")
	}
	// Copy every retained byte while holding the queue lock. Reconciliation
	// may free slabs as soon as the canonical writer runs; the worker never
	// dereferences those original pointers.
	q.mu.Lock()
	if q.closed || q.reconciling || q.bootstrapPending || q.epoch != r.Epoch || header.Number.Uint64() <= q.stamp.number {
		q.mu.Unlock()
		return errors.New("speculative queue epoch or canonical gate changed")
	}
	stamp := q.stamp
	copyQ, addresses, err := snapshotSpeculativeLanes(q)
	q.mu.Unlock()
	if err != nil {
		return err
	}
	var lease [8]byte
	binary.LittleEndian.PutUint64(lease[:], stamp.generation)
	binding := crypto.Keccak256Hash([]byte("gov5-speculative-queue-v1\x00"), r.Epoch[:], stamp.hash[:], lease[:], r.PreparedHash[:])
	ctx, cancel := context.WithCancel(s.ctx)
	slot := &speculativeQueueSlot{done: make(chan struct{}), cleanup: make(chan struct{}), cancel: cancel, epoch: r.Epoch, stamp: stamp, parent: header.ParentHash, number: header.Number.Uint64(), view: view, maxTx: r.MaxTransactions, maxBytes: r.MaxEncodedBytes, binding: binding}
	// Register the worker before exposing the slot to Close. A shutdown after
	// installation may wait on slot.done, so every installed slot must launch.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return errors.New("speculative queue owner closing")
	}
	s.wg.Add(1)
	s.mu.Unlock()
	s.specMu.Lock()
	if s.ctx.Err() != nil || s.specRecoveryEpoch == r.Epoch || s.specSlot != nil || s.specDraining != nil || s.specExecutionBlocks != 0 {
		s.specMu.Unlock()
		cancel()
		s.wg.Done()
		return errors.New("speculative queue slot or executor busy")
	}
	s.specSlot = slot
	s.specTimerWG.Add(1)
	slot.timer = time.AfterFunc(30*time.Second, func() {
		defer s.specTimerWG.Done()
		s.expireSpeculativeQueueSlot(slot)
	})
	s.specMu.Unlock()
	go func() {
		defer s.wg.Done()
		defer close(slot.done)
		defer func() {
			if recover() != nil {
				slot.mu.Lock()
				if slot.child != nil {
					slot.child.Close()
					slot.child = nil
				}
				slot.err = errors.New("speculative queue worker failed")
				slot.mu.Unlock()
			}
		}()
		parent, err := s.chain.ReadSpeculativeAppliedParentAccounts(ctx, slot.parent, slot.number-1, addresses)
		if err == nil && (view <= parent.View || header.GasLimit != parent.GasLimit || header.BaseFee == nil || !header.BaseFee.Eq(&parent.NextBaseFee)) {
			err = errors.New("speculative queue parent view/gas/fee mismatch")
		}
		var child *internal.SpeculativeAppliedChild
		if err == nil {
			var txs []*transaction.Transaction
			txs, err = s.selectQueueTransactions(copyQ, addresses, parent.Accounts, header, slot.maxTx, slot.maxBytes)
			if err == nil && len(txs) == 0 {
				err = ErrQueueBuildEmpty
			}
			if err == nil {
				s.seedSpeculativeQueueSenderMemos(copyQ, header, txs)
				selected := block.CopyHeader(header)
				selected.TxHash = block.TxRootAt(txs, selected.Time)
				selected.ResetHashCache()
				child, err = s.chain.BeginSpeculativeAppliedChild(ctx, slot.parent, slot.number-1, slot.binding, block.NewBlock(selected, txs).(*block.Block))
			}
		}
		slot.mu.Lock()
		if ctx.Err() != nil {
			if child != nil {
				child.Close()
			}
			slot.err = ctx.Err()
		} else {
			slot.child, slot.err = child, err
		}
		slot.mu.Unlock()
	}()
	return nil
}

// Every detached lane entry originated in strict signature admission, and its
// wire bytes were copied before canonical reconciliation could release slabs.
// Re-bind that proven sender only to the exact selected wire transaction.
// BeginSpeculativeAppliedChild transfers these memos across its owned decode.
func (s *Server) seedSpeculativeQueueSenderMemos(q *strictIngressQueue, header *block.Header, txs []*transaction.Transaction) {
	if q == nil || header == nil || header.Number == nil || !header.Number.IsUint64() || s.chain.Config() == nil || s.chain.Config().ChainID == nil {
		return
	}
	chainID := s.chain.Config().ChainID
	signer := transaction.MakeSignerWithTimestamp(s.chain.Config(), header.Number.ToBig(), header.Time)
	if !(transaction.NewEIP155Signer(chainID).Equal(signer) || transaction.NewEIP2930Signer(chainID).Equal(signer) || transaction.NewLondonSigner(chainID).Equal(signer)) {
		return
	}
	proofs := make(map[types.Hash]canonicalIngressSender, len(txs))
	for address, lane := range q.lanes {
		for _, entry := range lane.entries {
			proofs[entry.hash] = canonicalIngressSender{address: address, nonce: entry.nonce, raw: entry.slab.payload[entry.start:entry.end]}
		}
	}
	for _, tx := range txs {
		if tx == nil || tx.Type() != transaction.LegacyTxType || !tx.Protected() || tx.ChainId().ToBig().Cmp(chainID) != 0 || tx.Nonce() == ^uint64(0) {
			continue
		}
		raw, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			continue
		}
		proof, ok := proofs[crypto.Keccak256Hash(raw)]
		if ok && proof.nonce == tx.Nonce() && bytes.Equal(raw, proof.raw) {
			tx.SetSenderMemo(signer, proof.address)
		}
	}
}

func snapshotSpeculativeLanes(q *strictIngressQueue) (*strictIngressQueue, []types.Address, error) {
	out := &strictIngressQueue{lanes: make(map[types.Address]*ingressLane, len(q.lanes))}
	addresses := make([]types.Address, 0, len(q.lanes))
	bytesTotal := 0
	entriesTotal := 0
	for address, lane := range q.lanes {
		if len(lane.entries) == 0 {
			continue
		}
		addresses = append(addresses, address)
		copyLane := &ingressLane{entries: make([]*ingressEntry, 0, len(lane.entries))}
		for _, entry := range lane.entries {
			entriesTotal++
			if entriesTotal > maxSpeculativeQueueSnapshotEntries {
				return nil, nil, errors.New("speculative queue snapshot exceeds entry bound")
			}
			if entry == nil || entry.slab == nil || entry.start < 0 || entry.end <= entry.start || entry.end > len(entry.slab.payload) {
				return nil, nil, errors.New("speculative queue slab integrity mismatch")
			}
			bytesTotal += entry.end - entry.start
			if bytesTotal > maxSpeculativeQueueSnapshotBytes {
				return nil, nil, errors.New("speculative queue snapshot exceeds bound")
			}
			copyEntry := *entry
			copyEntry.slab = &ingressSlab{payload: bytes.Clone(entry.slab.payload[entry.start:entry.end])}
			copyEntry.start, copyEntry.end = 0, len(copyEntry.slab.payload)
			copyLane.entries = append(copyLane.entries, &copyEntry)
		}
		out.lanes[address] = copyLane
	}
	sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })
	return out, addresses, nil
}

func (s *Server) takeSpeculativeQueueChild(r QueueBuildRequest, header *block.Header, view hotstuff.ViewNumber) (*internal.SpeculativeAppliedChild, types.Hash, context.CancelFunc) {
	s.specMu.Lock()
	slot := s.specSlot
	if slot == nil || slot.epoch != r.Epoch || slot.parent != header.ParentHash || slot.number != header.Number.Uint64() || slot.view != view || slot.maxTx != r.MaxTransactions || slot.maxBytes != r.MaxEncodedBytes {
		s.specMu.Unlock()
		return nil, types.Hash{}, nil
	}
	select {
	case <-slot.done:
	default:
		s.specMu.Unlock()
		return nil, types.Hash{}, nil
	}
	q := s.ingress
	q.mu.Lock()
	stamp, dirty, epoch := q.stamp, q.reconciling || q.closed, q.epoch
	q.mu.Unlock()
	valid := !dirty && epoch == slot.epoch && ((stamp == slot.stamp && stamp.hash == slot.parent && stamp.number+1 == slot.number) || (s.specTransition != nil && s.specTransition.epoch == slot.epoch && s.specTransition.from == slot.stamp && s.specTransition.to == stamp && stamp.hash == slot.parent && stamp.number+1 == slot.number))
	if !valid {
		s.specMu.Unlock()
		return nil, types.Hash{}, nil
	}
	s.specSlot = nil
	s.specMu.Unlock()
	s.stopSpeculativeQueueTimer(slot)
	slot.mu.Lock()
	child, binding := slot.child, slot.binding
	slot.child = nil
	slot.mu.Unlock()
	return child, binding, slot.cancel
}

// Called under execute after the real prepared header has passed the public
// verifier and the real queue plan has selected its immutable transaction body.
// Complete independently compares every execution field except Extra and
// finalizes only under the actual QC-bearing header.
func (s *Server) completeSpeculativeQueuePlan(plan *queueBuildPlan, child *internal.SpeculativeAppliedChild, binding types.Hash) (BuildResult, error) {
	var template block.Block
	if err := template.Unmarshal(plan.input); err != nil {
		return BuildResult{}, err
	}
	templateHash := crypto.Keccak256Hash(plan.input)
	s.buildCalls.Add(1)
	s.prepared = nil
	built, executed, prepared, err := s.chain.CompleteSpeculativeAppliedChild(s.ctx, child, binding, &template, s.reuseOwnBuild)
	if err != nil {
		return BuildResult{}, err
	}
	var raw []byte
	if prepared != nil {
		raw = prepared.CopyUnsignedRLP()
	} else {
		raw, err = built.Marshal()
		if err != nil {
			return BuildResult{}, err
		}
	}
	if len(raw) > MaxBlockBytes || len(raw)+len(plan.input) > plan.bytes {
		return BuildResult{}, errors.New("speculative candidate exceeds reserved payload budget")
	}
	s.prepared = prepared
	return BuildResult{Identity: s.identity, TemplateHash: templateHash, BlockHash: built.Hash(), BlockRLP: raw, Result: executed}, nil
}
