package sharedvalidator

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

var ownCandidatePrefix = []byte("performance-own-candidate/")

type ownCandidate struct {
	View         hotstuff.ViewNumber
	Parent, Hash types.Hash
}
type pendingTransaction struct {
	tx    *transaction.Transaction
	bytes int
}

// Producer builds remotely and signs only in this validator. Per-view sealed
// candidates are durable before import/proposal; retries cannot swap payloads.
type Producer struct {
	replica            *Replica
	ctx                context.Context
	cancel             context.CancelFunc
	produceMu          sync.Mutex
	mu                 sync.Mutex
	started, closed    bool
	done               chan struct{}
	wake               chan types.Hash
	pending            map[types.Hash]pendingTransaction
	order              []types.Hash
	bytes              int
	pruned             uint64
	published          hotstuff.ViewNumber
	interval           time.Duration
	lastProduced       time.Time
	lastError          string
	ownerQueue         *OwnerQueueBinding
	speculativeEnabled bool
	speculativeBusy    bool
	speculativeParent  types.Hash
	speculativeView    hotstuff.ViewNumber
	speculativeCancel  context.CancelFunc
	productionStarted  bool
	recoveryOnly       bool
	drainDone          chan struct{}
	drainAcknowledged  bool
	freshBuilds        uint64
	wg                 sync.WaitGroup
}

func NewProducer(ctx context.Context, replica *Replica, interval time.Duration) (*Producer, error) {
	if ctx == nil || replica == nil || replica.engine == nil || replica.engine.Engine() == nil || replica.service == nil || interval < 0 {
		return nil, errors.New("remote producer requires initialized validator and nonnegative pacing")
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Producer{replica: replica, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan types.Hash, 1), pending: make(map[types.Hash]pendingTransaction), pruned: replica.CurrentBlock().Number64().Uint64(), interval: interval}, nil
}

// SubmitTransactions owns normalized Ethereum-wire copies; it does not trust
// caller sender fields. The actual builder still verifies signature/nonce/state.
// Queue capacity bounds encoded retention, not total decoded Go heap.
func (p *Producer) SubmitTransactions(txs []*transaction.Transaction) error {
	if p.OwnerQueueBinding() != nil {
		return ErrOwnerQueueSubmissionDisabled
	}
	if len(txs) > sharedexec.MaxTransactions {
		return errors.New("remote producer transaction batch too large")
	}
	raw := make([][]byte, len(txs))
	for i, tx := range txs {
		if tx == nil {
			return errors.New("nil remote producer transaction")
		}
		var err error
		raw[i], err = tx.EthEncoded()
		if err != nil {
			return err
		}
	}
	return p.submitRaw(context.Background(), raw, nil)
}

type admissionHead struct {
	hash   types.Hash
	number uint64
}

var ErrAdmissionHeadChanged = errors.New("canonical head changed; refresh status before resubmitting")

// SubmitRawTransactionsAtHead admits only signed Ethereum wire transactions.
// A replay against an already advanced head cannot poison the current pool.
func (p *Producer) SubmitRawTransactionsAtHead(ctx context.Context, raw [][]byte, head types.Hash, number uint64) error {
	if head == (types.Hash{}) {
		return errors.New("explicit canonical admission head required")
	}
	return p.submitRaw(ctx, raw, &admissionHead{head, number})
}

func (p *Producer) submitRaw(ctx context.Context, raws [][]byte, expected *admissionHead) error {
	if p.OwnerQueueBinding() != nil {
		return ErrOwnerQueueSubmissionDisabled
	}
	if ctx == nil || len(raws) > sharedexec.MaxTransactions {
		return errors.New("invalid transaction admission context/count")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	checkHead := func() error {
		if expected == nil {
			return nil
		}
		progress, err := p.replica.CanonicalProgress(ctx)
		if err != nil {
			return err
		}
		if progress.BlockHash != expected.hash || progress.Number != expected.number {
			return ErrAdmissionHeadChanged
		}
		return nil
	}
	if err := checkHead(); err != nil {
		return err
	}
	items := make(map[types.Hash]pendingTransaction, len(raws))
	order := make([]types.Hash, 0, len(raws))
	head := p.replica.CurrentBlock()
	if head == nil {
		return errors.New("remote producer canonical head unavailable")
	}
	period := p.replica.cfg.HotStuff.Period
	if period == 0 {
		period = 3
	}
	signer := transaction.MakeSignerWithTimestamp(p.replica.cfg, uint256.NewInt(head.Number64().Uint64()+1).ToBig(), head.Time()+period)
	inputBytes := 0
	for _, raw := range raws {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.ctx.Err(); err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > sharedexec.MaxBlockBytes || inputBytes > sharedexec.MaxBlockBytes-len(raw) {
			return errors.New("transaction admission wire budget exceeded")
		}
		inputBytes += len(raw)
		tx, err := transaction.DecodeEthereumTransaction(append([]byte(nil), raw...))
		if err != nil {
			return err
		}
		if _, err := transaction.RecoverSenderFromSig(signer, tx); err != nil {
			return err
		}
		hash := tx.Hash()
		if _, exists := items[hash]; !exists {
			items[hash] = pendingTransaction{tx, len(raw)}
			order = append(order, hash)
		}
	}
	// Signature work stays outside the replica lock. Recheck and enqueue while
	// canonicalization is excluded, closing the head-check/queue insertion gap.
	if expected != nil {
		p.replica.mu.Lock()
		defer p.replica.mu.Unlock()
		if err := checkHead(); err != nil {
			return err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("remote producer stopped")
	}
	if p.ownerQueue != nil {
		return ErrOwnerQueueSubmissionDisabled
	}
	added, count := 0, 0
	for hash, item := range items {
		if _, exists := p.pending[hash]; !exists {
			added += item.bytes
			count++
		}
	}
	if len(p.pending)+count > sharedexec.MaxTransactions || p.bytes+added > sharedexec.MaxBlockBytes {
		return errors.New("remote producer pending capacity full")
	}
	for _, hash := range order {
		if _, exists := p.pending[hash]; !exists {
			p.pending[hash] = items[hash]
			p.order = append(p.order, hash)
			p.bytes += items[hash].bytes
		}
	}
	return nil
}

func (p *Producer) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started || p.closed {
		return errors.New("remote producer starts once")
	}
	p.started = true
	p.wg.Add(1)
	go p.loop()
	return nil
}
func (p *Producer) Stop() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		<-p.done
		return
	}
	p.closed = true
	p.cancel()
	p.mu.Unlock()
	p.wg.Wait()
	p.produceMu.Lock()
	p.produceMu.Unlock()
	p.mu.Lock()
	p.pending = nil
	p.order = nil
	p.bytes = 0
	p.mu.Unlock()
	close(p.done)
}
func (p *Producer) TriggerBlockProduction(parent types.Hash) {
	select {
	case p.wake <- parent:
	default:
	}
}
func (p *Producer) CommitToCanonical(types.Hash) error {
	return errors.New("remote producer requires a public CommitQC; hash-only finality is disabled")
}
func (p *Producer) LastError() string { p.mu.Lock(); defer p.mu.Unlock(); return p.lastError }

func (p *Producer) loop() {
	defer p.wg.Done()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		var parent types.Hash
		retry := false
		select {
		case <-p.ctx.Done():
			return
		case parent = <-p.wake:
		case <-ticker.C:
			retry = true
		}
		if !p.replica.engine.IsCurrentLeader() {
			continue
		}
		ready := p.readyForProduction(p.replica.engine.Engine().CurrentView)
		if !ready {
			continue
		}
		if retry {
			// Timer retries must not bypass the Service's peer-lag and parent
			// gates. Its accepted request enqueues a normal wake event above.
			if err := p.replica.service.RequestPerformanceProduction(); err != nil {
				p.mu.Lock()
				p.lastError = err.Error()
				p.mu.Unlock()
			}
			continue
		}
		_, err := p.Produce(p.ctx, parent)
		p.mu.Lock()
		if err != nil {
			p.lastError = err.Error()
		} else {
			p.lastError = ""
		}
		p.mu.Unlock()
	}
}

// Read the engine view before taking the producer mutex: the engine may be
// waiting for its durable vote journal while status only needs producer fields.
// Produce still revalidates the view and leadership before building/proposing.
func (p *Producer) readyForProduction(currentView func() hotstuff.ViewNumber) bool {
	view := currentView()
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.published != view && time.Since(p.lastProduced) >= p.interval
}

func candidateKey(view hotstuff.ViewNumber) []byte {
	key := append([]byte(nil), ownCandidatePrefix...)
	var raw [8]byte
	binary.BigEndian.PutUint64(raw[:], uint64(view))
	return append(key, raw[:]...)
}

func (p *Producer) candidateTransactions(parent *block.Header) ([]*transaction.Transaction, error) {
	// Only canonical transactions leave the volatile pool. An uncommitted
	// ancestor merely excludes them from that parent's child, so abandoning
	// an uncommitted branch does not silently discard submitted transactions.
	head := p.replica.CurrentBlock()
	height := head.Number64().Uint64()
	p.mu.Lock()
	defer p.mu.Unlock()
	for number := p.pruned + 1; number <= height; number++ {
		b, err := p.replica.GetBlockByNumber(uint256.NewInt(number))
		if err != nil || b == nil {
			return nil, errors.New("remote producer canonical body missing")
		}
		for _, tx := range b.Body().Transactions() {
			hash := tx.Hash()
			if item, ok := p.pending[hash]; ok {
				p.bytes -= item.bytes
				delete(p.pending, hash)
			}
		}
		p.pruned = number
	}
	excluded := make(map[types.Hash]struct{})
	for current, steps := parent, 0; current.Hash() != head.Hash(); steps++ {
		if steps >= 256 || current.Number == nil || current.Number.Uint64() <= height {
			return nil, errors.New("remote producer parent does not extend local canonical prefix")
		}
		var b *block.Block
		err := p.replica.db.View(p.ctx, func(tx kv.Tx) error { var err error; b, err = rawdb.ReadBlockByHash(tx, current.Hash()); return err })
		if err != nil || b == nil {
			return nil, errors.New("remote producer uncommitted parent body unavailable")
		}
		for _, tx := range b.Transactions() {
			excluded[tx.Hash()] = struct{}{}
		}
		ih, err := p.replica.GetHeaderByHash(current.ParentHash)
		if err != nil {
			return nil, err
		}
		next, ok := ih.(*block.Header)
		if !ok || next == nil {
			return nil, errors.New("remote producer parent ancestry unavailable")
		}
		current = next
	}
	order := p.order[:0]
	var txs []*transaction.Transaction
	for _, hash := range p.order {
		item, exists := p.pending[hash]
		if !exists {
			continue
		}
		order = append(order, hash)
		if _, seen := excluded[hash]; !seen {
			txs = append(txs, item.tx)
		}
	}
	p.order = order
	return txs, nil
}

// Produce is a real one-view attempt, also used by the async BlockProducer
// loop. It may be retried after unavailable-parent or transport failures.
func (p *Producer) Produce(ctx context.Context, parentHash types.Hash) (produced *block.Block, produceErr error) {
	phases := newOperationPhases("produce", p.replica)
	phases.parent = parentHash
	defer func() { phases.finish(produceErr) }()
	opCtx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	ctx = opCtx
	phases.phase("produce_mu_wait")
	p.produceMu.Lock()
	defer p.produceMu.Unlock()
	phases.phase("parent_evidence")
	p.mu.Lock()
	p.productionStarted = true
	p.mu.Unlock()
	if err := p.ctx.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engine := p.replica.engine
	ce := engine.Engine()
	view := ce.CurrentView()
	phases.view = uint64(view)
	if !engine.IsCurrentLeader() {
		return nil, errors.New("remote producer is not current leader")
	}
	if parentHash == (types.Hash{}) {
		parentHash = p.replica.CurrentBlock().Hash()
	}
	p.mu.Lock()
	if p.speculativeBusy && (p.speculativeParent != parentHash || p.speculativeView != view) && p.speculativeCancel != nil {
		p.speculativeCancel()
	}
	p.mu.Unlock()
	ih, err := p.replica.GetHeaderByHash(parentHash)
	if err != nil {
		return nil, err
	}
	parent, ok := ih.(*block.Header)
	if !ok || parent == nil || parent.Number == nil || !p.replica.BlockApplied(parentHash, parent.Number.Uint64()) {
		return nil, errors.New("remote producer parent lacks bound execution evidence")
	}
	phases.parent, phases.number = parentHash, parent.Number.Uint64()+1
	phases.phase("candidate_wal_read")
	var candidate *block.Block
	if err := p.replica.db.View(ctx, func(tx kv.Tx) error {
		raw, err := tx.GetOne(modules.HotStuffState, candidateKey(view))
		if err != nil || len(raw) == 0 {
			return err
		}
		if len(raw) > 1024 {
			return errors.New("own candidate WAL oversized")
		}
		var record ownCandidate
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.View != view || record.Parent != parentHash {
			return errors.New("own candidate WAL view already bound to different parent")
		}
		candidate, err = rawdb.ReadBlockByHash(tx, record.Hash)
		if err != nil {
			return err
		}
		if candidate == nil || candidate.ParentHash() != parentHash {
			return errors.New("own candidate WAL body missing")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if candidate != nil {
		phases.path = "ownWAL"
	}
	persistCandidate := candidate == nil
	if candidate == nil {
		phases.phase("applied_child_rpc")
		applied, err := p.replica.client.AppliedChild(ctx, sharedexec.AppliedChildRequest{Identity: p.replica.identity, ParentHash: parentHash, Number: parent.Number.Uint64() + 1, View: view})
		if err != nil {
			return nil, err
		}
		phases.pause()
		if len(applied.BlockRLP) > 0 {
			phases.path = "appliedChild"
			phases.rawBytes = len(applied.BlockRLP)
			phases.phase("applied_child_unmarshal")
			candidate = new(block.Block)
			if err := candidate.Unmarshal(applied.BlockRLP); err != nil {
				return nil, err
			}
		}
	}
	if candidate == nil {
		if err := p.admitFreshBuild(); err != nil {
			return nil, err
		}
		phases.path = "newBuild"
		var result sharedexec.BuildResult
		if p.OwnerQueueBinding() != nil {
			phases.phase("queue_request")
			request, err := p.ownerQueueRequest(ctx, view, parent)
			if err != nil {
				return nil, err
			}
			if ce.CurrentView() != view || !engine.IsCurrentLeader() {
				return nil, errors.New("owner queue view changed before build request")
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			phases.phase("queue_build_rpc")
			built, err := p.replica.client.BuildFromQueue(ctx, request)
			if err != nil {
				return nil, err
			}
			result = built.BuildResult
		} else {
			phases.phase("local_template")
			txs, err := p.candidateTransactions(parent)
			if err != nil {
				return nil, err
			}
			header := &block.Header{ParentHash: parentHash, Number: uint256.NewInt(parent.Number.Uint64() + 1), GasLimit: parent.GasLimit, Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(0)}
			if p.replica.cfg.IsLondon(header.Number.Uint64()) {
				header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(p.replica.cfg, parent))
			}
			if err := engine.Prepare(p.replica, header); err != nil {
				return nil, err
			}
			header.TxHash = block.TxRootAt(txs, header.Time)
			template := block.NewBlock(header, txs).(*block.Block)
			raw, err := template.Marshal()
			if err != nil {
				return nil, err
			}
			phases.phase("build_rpc")
			result, err = p.replica.client.Build(ctx, sharedexec.BuildRequest{Identity: p.replica.identity, TemplateHash: crypto.Keccak256Hash(raw), TemplateRLP: raw})
			if err != nil {
				return nil, err
			}
		}
		phases.phase("unsigned_binding")
		if ce.CurrentView() != view || !engine.IsCurrentLeader() {
			return nil, errors.New("remote candidate view changed before signing")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		phases.rawBytes = len(result.BlockRLP)
		phases.phase("unsigned_unmarshal")
		var unsigned block.Block
		if err := unsigned.Unmarshal(result.BlockRLP); err != nil {
			return nil, err
		}
		phases.phase("seal_wait")
		sealed := make(chan block.IBlock, 1)
		if err := engine.Seal(p.replica, &unsigned, sealed, ctx.Done()); err != nil {
			return nil, err
		}
		select {
		case signed := <-sealed:
			candidate = signed.(*block.Block)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	phases.hash, phases.transactions = candidate.Hash(), len(candidate.Transactions())
	phases.phase("candidate_header_verify")
	if persistCandidate {
		if err := engine.VerifyPerformanceSealedHeader(p.replica, candidate.Header().(*block.Header)); err != nil {
			return nil, err
		}
		record, err := json.Marshal(ownCandidate{View: view, Parent: parentHash, Hash: candidate.Hash()})
		if err != nil {
			return nil, err
		}
		phases.phase("candidate_wal_write")
		if err := p.replica.db.Update(ctx, func(tx kv.RwTx) error {
			if existing, err := tx.GetOne(modules.HotStuffState, candidateKey(view)); err != nil {
				return err
			} else if len(existing) > 0 && string(existing) != string(record) {
				return errors.New("own candidate view cannot replace persisted payload")
			}
			if err := rawdb.WriteBlock(tx, candidate); err != nil {
				return err
			}
			if err := tx.Put(modules.HotStuffState, candidateKey(view), record); err != nil {
				return err
			}
			if p.OwnerQueueBinding() != nil {
				return tx.Delete(modules.HotStuffState, queueRequestKey(view))
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	phases.phase("sealed_header_verify")
	if err := engine.VerifyPerformanceSealedHeader(p.replica, candidate.Header().(*block.Header)); err != nil {
		return nil, err
	}
	sealedView, err := hotstuff.ExtractViewFromExtra(candidate.Header().(*block.Header).Extra)
	if err != nil || hotstuff.ViewNumber(sealedView) > view {
		return nil, errors.New("own candidate header is from a future view")
	}
	phases.phase("candidate_marshal")
	raw, err := candidate.Marshal()
	if err != nil {
		return nil, err
	}
	phases.rawBytes = len(raw)
	phases.phase("replica_import")
	result, err := p.replica.Import(ctx, raw)
	if err != nil {
		return nil, err
	}
	phases.durable = result.Durable
	phases.phase("proposal_gate")
	if !result.Durable {
		return nil, errors.New("remote candidate import remains accepted; retry")
	}
	if ce.CurrentView() != view || !engine.IsCurrentLeader() {
		return nil, errors.New("remote candidate view changed before proposal")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	phases.phase("notify_sealed")
	if err := engine.NotifyPerformanceBlockSealed(view, candidate.Hash(), candidate.TxHash(), parentHash); err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.published = view
	p.lastProduced = time.Now()
	p.mu.Unlock()
	phases.pause()
	return candidate, nil
}
