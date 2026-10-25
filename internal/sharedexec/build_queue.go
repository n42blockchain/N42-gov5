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

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

const MaxQueuePreparedBytes = 64 << 10
const MaxQueueBuildEncodedBytes = 32 << 20

var ErrQueueBuildExpired = errors.New("queue build plan expired; use current applied child and a new view")
var ErrQueueBuildConflict = errors.New("queue view already bound to another prepared request")
var ErrQueueBuildEmpty = errors.New("queue has no eligible transactions; retry the prepared request")

type QueueBuildRequest struct {
	Identity        Identity   `json:"identity"`
	Epoch           [16]byte   `json:"epoch"`
	PreparedHash    types.Hash `json:"prepared_hash"`
	PreparedRLP     []byte     `json:"prepared_rlp"`
	MaxTransactions int        `json:"max_transactions"`
	MaxEncodedBytes int        `json:"max_encoded_bytes"`
}
type QueueBuildResult struct {
	Epoch        [16]byte   `json:"epoch"`
	PreparedHash types.Hash `json:"prepared_hash"`
	BuildResult
}
type queueBuildPlan struct {
	key    types.Hash
	number uint64
	input  []byte
	result BuildResult
	bytes  int
}

func queueBuildKey(r QueueBuildRequest) types.Hash {
	var limits [8]byte
	binary.LittleEndian.PutUint32(limits[:4], uint32(r.MaxTransactions))
	binary.LittleEndian.PutUint32(limits[4:], uint32(r.MaxEncodedBytes))
	return crypto.Keccak256Hash([]byte("gov5-owner-queue-build-v1\x00"), r.Epoch[:], r.PreparedHash[:], limits[:])
}

// BuildFromQueue synchronously owns execute through selection, execution and
// publication. Request cancellation does not cancel an already selected owner
// execution or change its body. This first path does not pipeline execution.
func (s *Server) BuildFromQueue(ctx context.Context, r QueueBuildRequest) (response QueueBuildResult, err error) {
	phases := newOwnerOperationPhases("build_queue", s)
	phases.hash = r.PreparedHash
	defer func() { phases.finish(err) }()
	phases.phase("request_binding")
	if r.Identity != s.identity || r.MaxTransactions < 1 || r.MaxTransactions > MaxTransactions || r.MaxEncodedBytes < 1 || r.MaxEncodedBytes > MaxQueueBuildEncodedBytes || len(r.PreparedRLP) == 0 || len(r.PreparedRLP) > MaxQueuePreparedBytes {
		return QueueBuildResult{}, errors.New("queue build identity/limits/prepared size mismatch")
	}
	r.PreparedRLP = append([]byte(nil), r.PreparedRLP...)
	if crypto.Keccak256Hash(r.PreparedRLP) != r.PreparedHash {
		return QueueBuildResult{}, errors.New("queue build prepared hash mismatch")
	}
	if err := ctx.Err(); err != nil {
		return QueueBuildResult{}, err
	}
	s.mu.Lock()
	q := s.ingress
	if s.closed || q == nil {
		s.mu.Unlock()
		return QueueBuildResult{}, errors.New("queue build disabled or closed")
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	phases.phase("execute_wait")
	s.execute.Lock()
	defer s.execute.Unlock()
	s.beginSpeculativeExecutionExclusion()
	defer s.endSpeculativeExecutionExclusion()
	phases.phase("prepared_validate")
	if err := s.ctx.Err(); err != nil {
		return QueueBuildResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return QueueBuildResult{}, err
	}
	var prepared block.Block
	if err := prepared.Unmarshal(r.PreparedRLP); err != nil {
		return QueueBuildResult{}, err
	}
	header, ok := prepared.Header().(*block.Header)
	body, bodyOK := prepared.Body().(*block.Body)
	if !ok || header == nil || !bodyOK || body == nil || len(body.Transactions()) != 0 || body.IsFrameBody() || len(body.Verifiers) > 0 || len(body.Rewards) > 0 || len(body.ZkProof) > 0 || header.TxHash != block.BodyTxRoot(body, header.Time) {
		return QueueBuildResult{}, errors.New("queue build requires empty prepared body")
	}
	engine, ok := s.chain.Engine().(*hotstuff.HotStuff)
	if !ok {
		return QueueBuildResult{}, errors.New("queue build public HotStuff verifier missing")
	}
	if err := engine.ValidateUnsignedCandidateHeader(s.chain, header); err != nil {
		return QueueBuildResult{}, err
	}
	phases.parent, phases.number = header.ParentHash, header.Number.Uint64()
	view, err := hotstuff.ExtractViewFromExtra(header.Extra)
	if err != nil {
		return QueueBuildResult{}, err
	}
	q.mu.Lock()
	epoch, canonical, dirty := q.epoch, q.stamp.number, q.reconciling || q.closed
	q.mu.Unlock()
	if epoch != r.Epoch || dirty {
		return QueueBuildResult{}, errors.New("queue build epoch unavailable or reconciling")
	}
	if header.Number.Uint64() <= canonical || view <= s.queueExpiredView {
		return QueueBuildResult{}, ErrQueueBuildExpired
	}
	key := queueBuildKey(r)
	plan := s.queuePlans[view] // execute protects all plan reads and writes
	if plan != nil && plan.key != key {
		return QueueBuildResult{}, ErrQueueBuildConflict
	}
	if plan != nil && len(plan.result.BlockRLP) > 0 {
		phases.duplicate, phases.hash = true, plan.result.BlockHash
		phases.phase("response_copy")
		return queueBuildResponse(r, plan.result), nil
	}
	if plan == nil {
		// Public validation precedes account IO, reservations and view binding.
		phases.phase("lanes_snapshot_sort")
		q.mu.Lock()
		addresses := make([]types.Address, 0, len(q.lanes))
		for address, lane := range q.lanes {
			if len(lane.entries) > 0 {
				addresses = append(addresses, address)
			}
		}
		q.mu.Unlock()
		sort.Slice(addresses, func(i, j int) bool { return bytes.Compare(addresses[i][:], addresses[j][:]) < 0 })
		phases.phase("parent_accounts")
		parent, err := s.chain.ReadPerformanceParentAccounts(s.ctx, header.ParentHash, header.Number.Uint64()-1, addresses)
		if err != nil {
			return QueueBuildResult{}, err
		}
		if view <= parent.View || header.GasLimit != parent.GasLimit || header.BaseFee == nil || !header.BaseFee.Eq(&parent.NextBaseFee) {
			return QueueBuildResult{}, errors.New("queue build parent view/gas limit/base fee mismatch")
		}
		reserved := 2*(r.MaxEncodedBytes+MaxQueuePreparedBytes) + 4096
		if err := s.reserveQueueBuildBudget(reserved, true); err != nil {
			return QueueBuildResult{}, err
		}
		plan = &queueBuildPlan{key: key, number: header.Number.Uint64(), bytes: reserved}
		s.mu.Lock()
		// A regular Build may register between budget reservation and this
		// publication. Recheck its slot under the same mutex before inserting.
		if s.closed || len(s.builds)+len(s.queuePlans) >= s.capacity {
			s.buildBytes -= reserved
			s.mu.Unlock()
			return QueueBuildResult{}, errors.New("queue build slot busy or closing")
		}
		if s.queuePlans == nil {
			s.queuePlans = make(map[hotstuff.ViewNumber]*queueBuildPlan)
		}
		s.queuePlans[view] = plan
		s.mu.Unlock()
		// Materialization errors publish no plan. Once input exists, all retries
		// retain exactly that input, even when the execution attempt fails.
		phases.phase("select_decode")
		txs, err := s.selectQueueTransactions(q, addresses, parent.Accounts, header, r.MaxTransactions, r.MaxEncodedBytes)
		if err == nil && len(txs) == 0 {
			// An empty fresh selection is not a candidate. Use the materialization
			// cleanup below so the same prepared request can select later work.
			err = ErrQueueBuildEmpty
		}
		if err == nil {
			phases.transactions = len(txs)
			phases.phase("template_root_marshal")
			selected := block.CopyHeader(header)
			selected.TxHash = block.TxRootAt(txs, selected.Time)
			selected.ResetHashCache()
			plan.input, err = block.NewBlock(selected, txs).Marshal()
			if len(plan.input) > r.MaxEncodedBytes+MaxQueuePreparedBytes {
				err = errors.New("queue template exceeds reserved input allowance")
			}
		}
		if err != nil {
			s.mu.Lock()
			delete(s.queuePlans, view)
			s.buildBytes -= plan.bytes
			s.mu.Unlock()
			return QueueBuildResult{}, err
		}
	} else {
		if err := s.reserveQueueBuildBudget(len(plan.input)+4096, false); err != nil {
			return QueueBuildResult{}, err
		}
		plan.bytes += len(plan.input) + 4096
	}
	phases.phase("execute_build")
	result, buildErr := s.executeQueuePlanLocked(plan, r, header, view)
	if buildErr == nil && s.trace != nil {
		s.trace.mark("build_result", "ok", header.ParentHash, header.Number.Uint64(), uint64(view), r.Epoch, types.Hash{}, 0, r.PreparedHash, crypto.Keccak256Hash(plan.input), result.BlockHash)
	}
	phases.phase("publish_accounting")
	// Publish accounting while execute remains held: canonical pruning cannot
	// delete a plan and race a late worker completion which resurrects it.
	s.mu.Lock()
	s.buildBytes -= plan.bytes
	plan.result = result
	plan.bytes = len(plan.input) + len(result.BlockRLP)
	s.buildBytes += plan.bytes
	s.mu.Unlock()
	if buildErr != nil {
		return QueueBuildResult{}, buildErr
	}
	if err := ctx.Err(); err != nil {
		return QueueBuildResult{}, err
	}
	phases.hash = result.BlockHash
	phases.phase("response_copy")
	return queueBuildResponse(r, result), nil
}
func queueBuildResponse(r QueueBuildRequest, result BuildResult) QueueBuildResult {
	result.BlockRLP = append([]byte(nil), result.BlockRLP...)
	return QueueBuildResult{Epoch: r.Epoch, PreparedHash: r.PreparedHash, BuildResult: result}
}
func (s *Server) executeQueuePlanLocked(plan *queueBuildPlan, request QueueBuildRequest, header *block.Header, view hotstuff.ViewNumber) (result BuildResult, err error) {
	defer func() {
		if recover() != nil {
			s.prepared = nil
			result = BuildResult{}
			err = errors.New("queue candidate execution failed")
		}
	}()
	if s.speculativeEnabled {
		if child, binding, cancel := s.takeSpeculativeQueueChild(request, header, view); cancel != nil {
			s.trace.mark("take_speculative", "ready", header.ParentHash, header.Number.Uint64(), uint64(view), request.Epoch, binding, 0, request.PreparedHash, binding)
			defer cancel()
			if child != nil {
				defer child.Close()
				if speculative, specErr := s.completeSpeculativeQueuePlan(plan, child, binding); specErr == nil {
					s.trace.mark("consume_speculative", "ok", header.ParentHash, header.Number.Uint64(), uint64(view), request.Epoch, binding, 0, request.PreparedHash, binding, speculative.BlockHash)
					return speculative, nil
				}
			}
		}
		s.trace.mark("speculative_fallback", "ordinary", header.ParentHash, header.Number.Uint64(), uint64(view), request.Epoch, types.Hash{}, 0, request.PreparedHash, types.Hash{})
	}
	return s.executeBuildWithIngressLocked(plan.input, crypto.Keccak256Hash(plan.input), plan.bytes, &request.Epoch)
}

// Caller holds execute, but regular Build registration may run concurrently.
func (s *Server) reserveQueueBuildBudget(bytes int, newPlan bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	slots := len(s.builds) + len(s.queuePlans)
	if newPlan {
		slots++
	}
	for (slots > s.capacity || s.buildBytes+bytes > MaxBuildRetainedBytes) && len(s.buildCompleted) > 0 {
		key := s.buildCompleted[0]
		s.buildCompleted = s.buildCompleted[1:]
		if job := s.builds[key]; job != nil {
			s.buildBytes -= job.bytes
			delete(s.builds, key)
			slots--
		}
	}
	if slots > s.capacity || s.buildBytes+bytes > MaxBuildRetainedBytes {
		return errors.New("queue build plan capacity or encoded budget busy")
	}
	s.buildBytes += bytes
	return nil
}

func (s *Server) serveQueueBuild(w http.ResponseWriter, r *http.Request) {
	phases := newOwnerOperationPhases("build_queue_http", s)
	var operationErr error
	defer func() { phases.finish(operationErr) }()
	phases.phase("request_decode")
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 2*MaxQueuePreparedBytes+4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request QueueBuildRequest
	if operationErr = decoder.Decode(&request); operationErr != nil {
		http.Error(w, "invalid queue build request", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		operationErr = errors.New("one queue build request required")
		http.Error(w, "one queue build request required", http.StatusBadRequest)
		return
	}
	phases.hash = request.PreparedHash
	phases.phase("operation")
	result, err := s.BuildFromQueue(r.Context(), request)
	if err != nil {
		operationErr = err
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	phases.hash = result.BlockHash
	phases.phase("response_encode")
	w.Header().Set("Content-Type", "application/json")
	operationErr = json.NewEncoder(w).Encode(result)
}

// Selection is non-destructive. References are safe until execute is released:
// only canonical reconciliation removes slabs, and it also requires execute.
func (s *Server) selectQueueTransactions(q *strictIngressQueue, addresses []types.Address, accounts map[types.Address]account.StateAccount, header *block.Header, maxTx, maxBytes int) ([]*transaction.Transaction, error) {
	q.mu.Lock()
	if q.closed || q.reconciling {
		q.mu.Unlock()
		return nil, errors.New("queue selector unavailable")
	}
	lanes := make([][]*ingressEntry, len(addresses))
	for i, address := range addresses {
		if lane := q.lanes[address]; lane != nil {
			lanes[i] = lane.entries
		}
	}
	q.mu.Unlock()
	cursor := make([]int, len(addresses))
	next := make([]uint64, len(addresses))
	balance := make([]uint256.Int, len(addresses))
	for i, address := range addresses {
		a := accounts[address]
		if a.CodeHash != (types.Hash{}) && a.CodeHash != crypto.Keccak256Hash(nil) {
			lanes[i] = nil
		}
		next[i] = a.Nonce
		balance[i] = a.Balance
		for cursor[i] < len(lanes[i]) && lanes[i][cursor[i]].nonce < next[i] {
			cursor[i]++
		}
	}
	gasTx := header.GasLimit / 21000
	if uint64(maxTx) > gasTx {
		maxTx = int(gasTx)
	}
	var out []*transaction.Transaction
	encoded := 0
	for len(out) < maxTx {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		progress := false
		for i := range lanes {
			if len(out) == maxTx {
				break
			}
			if cursor[i] >= len(lanes[i]) {
				continue
			}
			e := lanes[i][cursor[i]]
			if e.nonce != next[i] || e.gasPrice.Cmp(header.BaseFee) < 0 || e.cost.Cmp(&balance[i]) > 0 {
				cursor[i] = len(lanes[i])
				continue
			}
			size := e.end - e.start
			if size+4 > maxBytes-encoded {
				cursor[i] = len(lanes[i])
				continue
			}
			raw := e.slab.payload[e.start:e.end]
			txn, err := transaction.DecodeEthereumTransaction(raw)
			if err != nil || txn.Hash() != e.hash || txn.Nonce() != next[i] {
				return nil, errors.New("queue retained transaction integrity mismatch")
			}
			out = append(out, txn)
			encoded += size + 4
			balance[i].Sub(&balance[i], &e.cost)
			next[i]++
			cursor[i]++
			progress = true
		}
		if !progress {
			break
		}
	}
	return out, nil
}

// expireQueuePlans runs only after owner-owned canonical reconciliation, still
// under execute. A new request checks the monotonic view/height floor before
// any cache hit, so removing a plan never permits it to be selected anew.
func (s *Server) expireQueuePlans(number uint64, view hotstuff.ViewNumber) {
	if view > s.queueExpiredView {
		s.queueExpiredView = view
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for v, plan := range s.queuePlans {
		if plan.number <= number {
			if v > s.queueExpiredView {
				s.queueExpiredView = v
			}
			s.buildBytes -= plan.bytes
			delete(s.queuePlans, v)
		}
	}
}
