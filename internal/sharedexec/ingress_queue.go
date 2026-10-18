package sharedexec

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

var ErrIngressSnapshotChanged = errors.New("strict ingress canonical snapshot changed; retry original frame")
var ErrIngressCreditsExhausted = errors.New("strict ingress credits exhausted")

type StrictIngressOptions struct {
	Manifest        []byte
	MaxTransactions int
	MaxEncodedBytes int
}

type StrictIngressStatus struct {
	Identity             Identity
	Epoch                [16]byte
	ManifestHash         types.Hash
	CanonicalHash        types.Hash
	CanonicalNumber      uint64
	CanonicalGeneration  uint64
	Reconciling          bool
	PendingTransactions  int
	RetainedEncodedBytes int
	ReservedTransactions int
	ReservedEncodedBytes int
	CryptoVerified       uint64
	NewlyAdmitted        uint64
}

type StrictIngressAck struct {
	Status        StrictIngressStatus
	NewlyAdmitted int
	Duplicates    int
}

type ingressSlab struct {
	payload    []byte
	references int
}
type ingressEntry struct {
	slab       *ingressSlab
	start, end int
	nonce      uint64
	hash       types.Hash
	cost       uint256.Int
	gasPrice   uint256.Int
}
type ingressLane struct {
	entries []*ingressEntry
	next    uint64
	cost    uint256.Int
}
type ingressStamp struct {
	generation, number uint64
	hash               types.Hash
}
type ingressFrameRecord struct {
	id    CorpusFrameID
	hash  types.Hash
	count int
	bytes int
}
type strictIngressQueue struct {
	mu                                        sync.Mutex
	descriptor                                CorpusDescriptor
	epoch                                     [16]byte
	manifestHash                              types.Hash
	stamp                                     ingressStamp
	reconciling                               bool
	bootstrapPending                          bool
	closed                                    bool
	maxTx, maxBytes                           int
	pending, bytes, reservedTx, reservedBytes int
	lanes                                     map[types.Address]*ingressLane
	recent                                    [CorpusMaxShards][]ingressFrameRecord
	highest                                   [CorpusMaxShards]uint64
	seen                                      [CorpusMaxShards]bool
	verified                                  atomic.Uint64
	admitted                                  uint64
	transportListening                        bool
	transportServing                          bool
	resume                                    *resumeIngressSession // immutable startup plan; counters under mu
}

// EnableStrictIngress is startup-only. The server must exclusively own all
// canonical writes. It admits protected legacy transfers through direct signer
// recovery; offline corpus verification and sender memo caches grant no trust.
// Producer queue selection is a separate integration.
func (s *Server) EnableStrictIngress(options StrictIngressOptions) error {
	manifest, err := DecodeCorpusManifest(options.Manifest)
	if err != nil {
		return err
	}
	if manifest.Descriptor.Identity != s.identity || options.MaxTransactions < 1 || options.MaxTransactions > 2_000_000 || options.MaxEncodedBytes < 1 || options.MaxEncodedBytes > 512<<20 {
		return errors.New("invalid strict ingress identity or bounded credits")
	}
	if _, ok := s.chain.Engine().(*hotstuff.HotStuff); !ok || s.chain.Config().HotStuff == nil || len(s.chain.Config().HotStuff.Validators) != 7 {
		return errors.New("strict ingress requires fixed-seven public HotStuff owner")
	}
	s.execute.Lock()
	defer s.execute.Unlock()
	s.mu.Lock()
	idle := !s.closed && !s.resumeArming && s.ingress == nil && len(s.jobs) == 0 && len(s.builds) == 0
	s.mu.Unlock()
	if !idle {
		return errors.New("strict ingress requires unused owner")
	}
	snapshot, err := s.chain.ReadPerformanceCanonicalAccounts(s.ctx, nil)
	bootstrap := errors.Is(err, internal.ErrPerformanceCanonicalNotApplied)
	if err != nil && !bootstrap {
		return err
	}
	// This exact typed outcome carries verified canonical metadata only. No
	// speculative PlainState nonce becomes an admission baseline. Keep new
	// admission/builds closed while validators replay existing certificates or
	// repropose an already applied child. Recovery is not guaranteed without a
	// usable certificate/child; no new candidate is built through this gate.
	q := &strictIngressQueue{descriptor: manifest.Descriptor, manifestHash: types.Hash(sha256.Sum256(options.Manifest)), maxTx: options.MaxTransactions, maxBytes: options.MaxEncodedBytes, lanes: make(map[types.Address]*ingressLane), stamp: ingressStamp{hash: snapshot.Hash, number: snapshot.Number}, reconciling: bootstrap, bootstrapPending: bootstrap}
	if _, err := rand.Read(q.epoch[:]); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.resumeArming {
		return errors.New("execution service closed or initializing resume")
	}
	s.ingress = q
	return nil
}

func (q *strictIngressQueue) status(identity Identity) StrictIngressStatus {
	return StrictIngressStatus{Identity: identity, Epoch: q.epoch, ManifestHash: q.manifestHash, CanonicalHash: q.stamp.hash, CanonicalNumber: q.stamp.number, CanonicalGeneration: q.stamp.generation, Reconciling: q.reconciling, PendingTransactions: q.pending, RetainedEncodedBytes: q.bytes, ReservedTransactions: q.reservedTx, ReservedEncodedBytes: q.reservedBytes, CryptoVerified: q.verified.Load(), NewlyAdmitted: q.admitted}
}

func (s *Server) StrictIngressStatus() (StrictIngressStatus, error) {
	s.mu.Lock()
	q := s.ingress
	s.mu.Unlock()
	if q == nil {
		return StrictIngressStatus{}, errors.New("strict ingress disabled")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.status(s.identity), nil
}

type ingressVerified struct {
	sender     types.Address
	nonce      uint64
	hash       types.Hash
	start, end int
	cost       uint256.Int
	gasPrice   uint256.Int
}

type ingressReservation struct {
	queue               *strictIngressQueue
	transactions, bytes int
	held                bool
	stamp               ingressStamp
}

func (r *ingressReservation) releaseLocked() {
	if r.held {
		r.queue.reservedTx -= r.transactions
		r.queue.reservedBytes -= r.bytes
		r.held = false
	}
}
func (r *ingressReservation) release() {
	r.queue.mu.Lock()
	defer r.queue.mu.Unlock()
	r.releaseLocked()
}

// ingressFrameLocked is checked again at publication: multiple requests can
// finish signature verification under the same canonical stamp concurrently.
func (q *strictIngressQueue) ingressFrameLocked(frame CorpusFrame, size int) (int, bool, error) {
	for _, previous := range q.recent[frame.Shard] {
		if previous.id == frame.CorpusFrameID {
			if previous.hash != frame.PayloadHash || previous.count != int(frame.Transactions) || previous.bytes != size {
				return 0, false, errors.New("strict ingress frame identity changed payload")
			}
			return previous.count, true, nil
		}
	}
	if q.seen[frame.Shard] && q.highest[frame.Shard] >= 32 && frame.Sequence <= q.highest[frame.Shard]-32 {
		return 0, false, errors.New("strict ingress frame expired")
	}
	return 0, false, nil
}

// AdmitStrictFrame owns one payload copy under the in-flight byte budget and
// publishes it only after the whole frame passes. Its ACK means online
// authorization and volatile admission, never commitment.
func (s *Server) AdmitStrictFrame(ctx context.Context, identity Identity, epoch [16]byte, frame CorpusFrame) (StrictIngressAck, error) {
	s.mu.Lock()
	q := s.ingress
	if s.closed || q == nil || q.resume != nil || identity != s.identity {
		s.mu.Unlock()
		return StrictIngressAck{}, errors.New("strict ingress disabled or identity mismatch")
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	if err := frame.Validate(q.descriptor); err != nil {
		return StrictIngressAck{}, err
	}
	reservation, known, err := s.reserveStrictIngress(q, epoch, frame, len(frame.Payload))
	if err != nil {
		return StrictIngressAck{}, err
	}
	if known != nil {
		return *known, nil
	}
	defer reservation.release()
	frame.Payload = append([]byte(nil), frame.Payload...)
	return s.verifyReservedIngress(ctx, q, frame, reservation)
}

// reserveStrictIngress runs on a validated bounded header before a transport
// allocates or reads payload bytes. A duplicate acknowledges an existing result.
func (s *Server) reserveStrictIngress(q *strictIngressQueue, epoch [16]byte, frame CorpusFrame, size int) (*ingressReservation, *StrictIngressAck, error) {
	id, err := q.descriptor.ID()
	if err != nil {
		return nil, nil, err
	}
	_, _, expected, err := q.descriptor.FrameRange(frame.Shard, frame.Sequence)
	if err != nil || frame.CorpusID != id || frame.Transactions != expected || size < int(frame.Transactions)*5 || size > CorpusMaxFramePayload {
		return nil, nil, errors.New("strict ingress header identity/range/size mismatch")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if epoch != q.epoch || q.closed || q.resume != nil || s.ctx.Err() != nil {
		return nil, nil, errors.New("strict ingress epoch unavailable")
	}
	if q.reconciling {
		return nil, nil, ErrIngressSnapshotChanged
	}
	duplicateCount, known, lookupErr := q.ingressFrameLocked(frame, size)
	if lookupErr != nil {
		return nil, nil, lookupErr
	}
	if known {
		ack := StrictIngressAck{Status: q.status(s.identity), Duplicates: duplicateCount}
		return nil, &ack, nil
	}
	count := int(frame.Transactions)
	if count > q.maxTx-q.pending-q.reservedTx || size > q.maxBytes-q.bytes-q.reservedBytes {
		return nil, nil, ErrIngressCreditsExhausted
	}
	q.reservedTx += count
	q.reservedBytes += size
	return &ingressReservation{queue: q, transactions: count, bytes: size, held: true, stamp: q.stamp}, nil, nil
}

func (s *Server) verifyReservedIngress(ctx context.Context, q *strictIngressQueue, frame CorpusFrame, reservation *ingressReservation) (StrictIngressAck, error) {
	if err := frame.Validate(q.descriptor); err != nil {
		return StrictIngressAck{}, err
	}
	count := int(frame.Transactions)
	verified := make([]ingressVerified, 0, count)
	_, firstNonce, _, _ := q.descriptor.FrameRange(frame.Shard, frame.Sequence)
	addresses := make([]types.Address, 0, count*2)
	signer := transaction.NewEIP155Signer(s.chain.Config().ChainID)
	offset := 0
	err := WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		tx, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			return err
		}
		to := tx.To()
		if tx.Type() != transaction.LegacyTxType || !tx.Protected() || tx.ChainId().ToBig().Cmp(s.chain.Config().ChainID) != 0 || to == nil || tx.Gas() != 21000 || len(tx.Data()) != 0 || tx.Nonce() == ^uint64(0) {
			return errors.New("strict ingress supports protected simple legacy transfers only")
		}
		if tx.Nonce() != firstNonce+uint64(len(verified)) {
			return errors.New("strict ingress frame nonce range mismatch")
		}
		from, err := signer.Sender(tx)
		if err != nil {
			return err
		}
		if len(verified) > 0 && verified[0].sender != from {
			return errors.New("strict ingress frame must contain one recovered sender")
		}
		q.verified.Add(1)
		var cost uint256.Int
		if _, overflow := cost.MulOverflow(tx.GasPrice(), uint256.NewInt(tx.Gas())); overflow {
			return errors.New("strict ingress cost overflow")
		}
		if _, overflow := cost.AddOverflow(&cost, tx.Value()); overflow {
			return errors.New("strict ingress cost overflow")
		}
		start := offset + 4
		offset = start + len(raw)
		verified = append(verified, ingressVerified{sender: from, nonce: tx.Nonce(), hash: tx.Hash(), start: start, end: offset, cost: cost, gasPrice: *tx.GasPrice()})
		addresses = append(addresses, from, *to)
		return nil
	})
	if err != nil {
		return StrictIngressAck{}, err
	}
	snapshot, err := s.chain.ReadPerformanceCanonicalAccounts(ctx, addresses)
	if err != nil {
		if errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) {
			return StrictIngressAck{}, ErrIngressSnapshotChanged
		}
		return StrictIngressAck{}, err
	}
	for i := 1; i < len(addresses); i += 2 {
		a := snapshot.Accounts[addresses[i]]
		if a.CodeHash != (types.Hash{}) && a.CodeHash != crypto.Keccak256Hash(nil) {
			return StrictIngressAck{}, errors.New("strict ingress recipient is not EOA")
		}
	}
	return s.admitVerifiedIngress(ctx, q, frame, reservation.stamp, snapshot, verified, reservation)
}

func (s *Server) admitVerifiedIngress(ctx context.Context, q *strictIngressQueue, frame CorpusFrame, stamp ingressStamp, snapshot internal.PerformanceCanonicalAccounts, verified []ingressVerified, reservation *ingressReservation) (StrictIngressAck, error) {
	count, size := int(frame.Transactions), len(frame.Payload)
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return StrictIngressAck{}, err
	}
	if q.closed || s.ctx.Err() != nil || q.reconciling || q.stamp != stamp || snapshot.Hash != stamp.hash || snapshot.Number != stamp.number {
		return StrictIngressAck{}, ErrIngressSnapshotChanged
	}
	duplicates, known, err := q.ingressFrameLocked(frame, size)
	if err != nil {
		return StrictIngressAck{}, err
	}
	if known {
		reservation.releaseLocked()
		status := q.status(s.identity)
		return StrictIngressAck{Status: status, Duplicates: duplicates}, nil
	}
	// Work on copies: a gap/conflict late in a frame cannot leave earlier
	// transactions, sender lanes, nonce cursors or budget changes behind.
	lanes := make(map[types.Address]*ingressLane)
	newEntries := make([]*ingressEntry, 0, count)
	newSenders := 0
	duplicates = 0
	slab := &ingressSlab{}
	for _, v := range verified {
		if v.gasPrice.Lt(&snapshot.NextBaseFee) {
			return StrictIngressAck{}, errors.New("strict ingress gas price below canonical next base fee")
		}
		a := snapshot.Accounts[v.sender]
		if a.CodeHash != (types.Hash{}) && a.CodeHash != crypto.Keccak256Hash(nil) {
			return StrictIngressAck{}, errors.New("strict ingress sender is not EOA")
		}
		lane := lanes[v.sender]
		if lane == nil {
			if existing := q.lanes[v.sender]; existing != nil {
				lane = &ingressLane{entries: append([]*ingressEntry(nil), existing.entries...), next: existing.next, cost: existing.cost}
			} else {
				lane = &ingressLane{next: a.Nonce}
				newSenders++
			}
			lanes[v.sender] = lane
		}
		if v.nonce < a.Nonce {
			return StrictIngressAck{}, errors.New("strict ingress canonical nonce is stale")
		}
		if v.nonce < lane.next {
			if len(lane.entries) == 0 || v.nonce < lane.entries[0].nonce {
				return StrictIngressAck{}, errors.New("strict ingress nonce unavailable")
			}
			index := v.nonce - lane.entries[0].nonce
			if index >= uint64(len(lane.entries)) || lane.entries[index].hash != v.hash {
				return StrictIngressAck{}, errors.New("strict ingress nonce replacement rejected")
			}
			duplicates++
			continue
		}
		if v.nonce != lane.next {
			return StrictIngressAck{}, errors.New("strict ingress nonce gap")
		}
		if _, overflow := lane.cost.AddOverflow(&lane.cost, &v.cost); overflow || lane.cost.Gt(&a.Balance) {
			return StrictIngressAck{}, errors.New("strict ingress insufficient canonical balance")
		}
		entry := &ingressEntry{slab: slab, start: v.start, end: v.end, nonce: v.nonce, hash: v.hash, cost: v.cost, gasPrice: v.gasPrice}
		lane.entries = append(lane.entries, entry)
		lane.next++
		newEntries = append(newEntries, entry)
	}
	if len(q.lanes)+newSenders > int(q.descriptor.SenderCount) {
		return StrictIngressAck{}, errors.New("strict ingress sender bound exceeded")
	}
	if len(newEntries) > 0 {
		slab.payload = frame.Payload
		slab.references = len(newEntries)
		q.bytes += size
		q.pending += len(newEntries)
		q.admitted += uint64(len(newEntries))
		for from, lane := range lanes {
			q.lanes[from] = lane
		}
	}
	recent := append(q.recent[frame.Shard], ingressFrameRecord{id: frame.CorpusFrameID, hash: frame.PayloadHash, count: count, bytes: size})
	if len(recent) > 32 {
		recent = recent[len(recent)-32:]
	}
	q.recent[frame.Shard] = recent
	if !q.seen[frame.Shard] || frame.Sequence > q.highest[frame.Shard] {
		q.highest[frame.Shard] = frame.Sequence
		q.seen[frame.Shard] = true
	}
	reservation.releaseLocked()
	status := q.status(s.identity)
	return StrictIngressAck{Status: status, NewlyAdmitted: len(newEntries), Duplicates: duplicates}, nil
}

// ingressTransactions is a bounded, non-destructive test/integration selector.
// BuildFromQueue will additionally bind actual-applied parent accounts/leases.
func (s *Server) ingressTransactions(limit int) ([][]byte, error) {
	s.mu.Lock()
	q := s.ingress
	s.mu.Unlock()
	if q == nil || limit < 1 || limit > MaxTransactions {
		return nil, errors.New("strict ingress selector unavailable")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.reconciling {
		return nil, errors.New("strict ingress reconciling")
	}
	var out [][]byte
	for _, lane := range q.lanes {
		for _, e := range lane.entries {
			out = append(out, append([]byte(nil), e.slab.payload[e.start:e.end]...))
			if len(out) == limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func (s *Server) reconcileStrictIngress(phases *ownerOperationPhases) error {
	q := s.ingress
	if q == nil {
		return nil
	}
	q.mu.Lock()
	stamp := q.stamp
	bootstrap := q.bootstrapPending
	q.mu.Unlock()
	floors := make(map[types.Address]uint64)
	var head types.Hash
	var number uint64
	var canonicalView hotstuff.ViewNumber
	phases.phase("ingress_read_decode")
	err := s.chain.DB().View(s.ctx, func(tx kv.Tx) error {
		head = rawdb.ReadHeadBlockHash(tx)
		n := rawdb.ReadHeaderNumber(tx, head)
		if n == nil || *n < stamp.number {
			return errors.New("strict ingress canonical head regressed")
		}
		number = *n
		if number > 0 {
			header := rawdb.ReadHeader(tx, head, number)
			if header == nil {
				return errors.New("queue canonical view header missing")
			}
			view, viewErr := hotstuff.ExtractViewFromExtra(header.Extra)
			if viewErr != nil {
				return viewErr
			}
			canonicalView = view
		}
		previous := stamp.hash
		for height := stamp.number + 1; height <= number; height++ {
			phases.phase("ingress_read_decode")
			hash, err := rawdb.ReadCanonicalHash(tx, height)
			if err != nil {
				return err
			}
			b, err := rawdb.ReadBlockByHash(tx, hash)
			if err != nil {
				return err
			}
			if b == nil || b.Number64().Uint64() != height || b.ParentHash() != previous || b.Hash() != hash {
				return errors.New("strict ingress canonical body missing or inconsistent")
			}
			signer := transaction.MakeSignerWithTimestamp(s.chain.Config(), b.Number64().ToBig(), b.Time())
			txs := b.Transactions()
			phases.phase("ingress_sender_recovery")
			senders, err := recoverCanonicalSenders(s.ctx, txs, signer, runtime.GOMAXPROCS(0))
			if err != nil {
				return err
			}
			phases.phase("ingress_floors")
			for index, txn := range txs {
				from := senders[index]
				if next := txn.Nonce() + 1; next > floors[from] {
					floors[from] = next
				}
			}
			previous = hash
		}
		if previous != head {
			return errors.New("strict ingress canonical prefix changed")
		}
		return nil
	})
	if err != nil {
		return err
	}
	ready := true
	if bootstrap {
		phases.phase("ingress_bootstrap_snapshot")
		// A duplicate or older valid QC may advance metadata while actual state
		// is still ahead. Acknowledge that QC, retaining the startup gate until
		// one real canonical PlainState snapshot matches this canonical tuple.
		snapshot, err := s.chain.ReadPerformanceCanonicalAccounts(s.ctx, nil)
		if errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) {
			ready = false
		} else if err != nil {
			return err
		} else if snapshot.Hash != head || snapshot.Number != number {
			return errors.New("strict ingress startup canonical snapshot changed")
		}
	}
	phases.phase("ingress_queue_wait")
	q.mu.Lock()
	phases.phase("ingress_queue_apply")
	for from, floor := range floors {
		lane := q.lanes[from]
		if lane == nil {
			continue
		}
		removed := 0
		for _, e := range lane.entries {
			if e.nonce >= floor {
				break
			}
			lane.cost.Sub(&lane.cost, &e.cost)
			q.pending--
			e.slab.references--
			if e.slab.references == 0 {
				q.bytes -= len(e.slab.payload)
				e.slab.payload = nil
			}
			lane.entries[removed] = nil
			removed++
		}
		if removed > 0 {
			// A middle subslice retains the original backing array even when
			// cap becomes small. Keep metadata proportional to live entries.
			remaining := lane.entries[removed:]
			lane.entries = append([]*ingressEntry(nil), remaining...)
		}
		if lane.next < floor {
			lane.next = floor
		}
	}
	q.stamp.hash = head
	q.stamp.number = number
	q.bootstrapPending = bootstrap && !ready
	q.reconciling = q.bootstrapPending
	q.mu.Unlock()
	phases.phase("ingress_expire_queue_plans")
	s.expireQueuePlans(number, canonicalView)
	return nil
}
