package sharedexec

import (
	"context"
	"errors"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
)

// CanonicalSkipped always describes the plan's fixed historical prefix. It is
// not new admission or a volatile duplicate. A duplicate receipt reports zero
// NewlyAdmitted and the immutable PriorAdmission from this epoch's first ACK.
type ResumeIngressAck struct {
	Status           StrictIngressStatus
	PlanID           types.Hash
	CanonicalSkipped int
	NewlyAdmitted    int
	PriorAdmission   int
	Duplicate        bool
	Lifetime         *BoundedResumeLifetime `json:",omitempty"`
}

type resumeFrameReceipt struct{ skipped, admitted int }

type resumeReservation struct {
	*ingressReservation
	session      *resumeIngressSession
	frame        ResumeFrame
	lifetimeHeld bool
}

func (r *resumeIngressSession) frame(id CorpusFrameID) (ResumeFrame, bool) {
	if r.window != nil {
		f, ok := r.window.frames[id]
		return boundedResumeOriginalFrame(f), ok
	}
	for _, frame := range r.plan.Frames {
		if frame.ID == id {
			return frame, true
		}
	}
	return ResumeFrame{}, false
}
func (s *Server) resumeReceiptAck(q *strictIngressQueue, r *resumeIngressSession, receipt resumeFrameReceipt, duplicate bool) ResumeIngressAck {
	newCount := receipt.admitted
	if duplicate {
		newCount = 0
	}
	ack := ResumeIngressAck{Status: q.status(s.identity), PlanID: r.planID, CanonicalSkipped: receipt.skipped, NewlyAdmitted: newCount, PriorAdmission: receipt.admitted, Duplicate: duplicate}
	if r.window != nil {
		ack.Lifetime = r.window.status()
	}
	return ack
}

// AdmitResumeFrame is an explicit sealed-plan path. Legacy admission never
// accepts a canonical prefix; the whole original frame is still authenticated.
func (s *Server) AdmitResumeFrame(ctx context.Context, identity Identity, planID types.Hash, epoch [16]byte, frame CorpusFrame) (ResumeIngressAck, error) {
	return s.admitResumeFrame(ctx, identity, planID, epoch, frame, false)
}

func (s *Server) admitResumeFrame(ctx context.Context, identity Identity, planID types.Hash, epoch [16]byte, frame CorpusFrame, bounded bool) (ResumeIngressAck, error) {
	if ctx == nil {
		return ResumeIngressAck{}, errors.New("active sealed resume context required")
	}
	s.mu.Lock()
	q := s.ingress
	if s.closed || q == nil || q.resume == nil || (q.resume.window != nil) != bounded || identity != s.identity {
		s.mu.Unlock()
		return ResumeIngressAck{}, errors.New("sealed resume ingress unavailable")
	}
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	reservation, err := s.reserveResumeIngress(q, planID, epoch, frame, len(frame.Payload))
	if err != nil {
		return ResumeIngressAck{}, err
	}
	defer reservation.release()
	frame.Payload = append([]byte(nil), frame.Payload...)
	return s.verifyReservedResume(ctx, q, frame, reservation)
}

func (s *Server) reserveResumeIngress(q *strictIngressQueue, planID types.Hash, epoch [16]byte, frame CorpusFrame, size int) (*resumeReservation, error) {
	r := q.resume
	if r == nil || r.planID != planID {
		return nil, errors.New("sealed resume plan differs")
	}
	selected, ok := r.frame(frame.CorpusFrameID)
	if !ok || selected.PayloadHash != frame.PayloadHash || int(selected.PayloadBytes) != size || frame.Transactions != 500 {
		return nil, errors.New("sealed resume frame outside exact original whitelist")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || s.ctx.Err() != nil || epoch != q.epoch {
		return nil, errors.New("sealed resume epoch unavailable")
	}
	if q.reconciling {
		return nil, ErrIngressSnapshotChanged
	}
	count := int(selected.NewlyAdmitted)
	_, known := r.receipts[frame.CorpusFrameID]
	if known {
		count = 0
	}
	if r.window != nil {
		if err := r.window.checkReservation(selected, known); err != nil {
			return nil, err
		}
	}
	if count > q.maxTx-q.pending-q.reservedTx || size > q.maxBytes-q.bytes-q.reservedBytes {
		return nil, ErrIngressCreditsExhausted
	}
	q.reservedTx += count
	q.reservedBytes += size
	reservation := &resumeReservation{ingressReservation: &ingressReservation{queue: q, transactions: count, bytes: size, held: true, stamp: q.stamp}, session: r, frame: selected}
	if r.window != nil && !known {
		r.window.reservedFrames++
		r.window.reservedTransactions += 500
		r.window.reservedBytes += uint64(CorpusWireHeaderSize) + uint64(selected.PayloadBytes)
		r.window.reservedNew += uint64(selected.NewlyAdmitted)
		reservation.lifetimeHeld = true
	}
	return reservation, nil
}

func (s *Server) verifyReservedResume(ctx context.Context, q *strictIngressQueue, frame CorpusFrame, r *resumeReservation) (ResumeIngressAck, error) {
	if frame.CorpusFrameID != r.frame.ID || frame.PayloadHash != r.frame.PayloadHash || len(frame.Payload) != int(r.frame.PayloadBytes) || frame.Transactions != 500 {
		return ResumeIngressAck{}, errors.New("sealed resume body differs from its reserved original header")
	}
	if err := frame.Validate(q.descriptor); err != nil {
		return ResumeIngressAck{}, err
	}
	sender := r.session.plan.Senders[r.frame.SenderIndex]
	signer := transaction.NewEIP155Signer(s.chain.Config().ChainID)
	verified := make([]ingressVerified, 0, r.frame.NewlyAdmitted)
	addresses := []types.Address{sender.Address}
	_, firstNonce, count, rangeErr := q.descriptor.FrameRange(frame.Shard, frame.Sequence)
	if rangeErr != nil || count != 500 || (r.session.window == nil && firstNonce != 0) {
		return ResumeIngressAck{}, errors.New("sealed resume original range invalid")
	}
	nonce := firstNonce
	offset := 0
	err := WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.session.window != nil && len(raw) > 102 {
			return errors.New("bounded resume original transaction exceeds recipe byte cap")
		}
		from, err := resumeSourceSender(raw, q.descriptor, signer, sender.Index, nonce)
		if err != nil || from != sender.Address {
			return errors.New("sealed resume original signature/sender/recipe differs")
		}
		q.verified.Add(1)
		start := offset + 4
		offset = start + len(raw)
		if nonce-firstNonce >= uint64(r.frame.CanonicalSkipped) {
			tx, err := transaction.DecodeEthereumTransaction(raw)
			if err != nil {
				return err
			}
			verified = append(verified, ingressVerified{sender: from, nonce: nonce, hash: tx.Hash(), start: start, end: offset, cost: *uint256.NewInt(21001), gasPrice: *uint256.NewInt(1)})
			addresses = append(addresses, *tx.To())
		}
		nonce++
		return nil
	})
	if err != nil {
		return ResumeIngressAck{}, err
	}
	if nonce-firstNonce != 500 || len(verified) != int(r.frame.NewlyAdmitted) {
		return ResumeIngressAck{}, errors.New("sealed resume original range differs")
	}
	// A known receipt still requires all 500 original signatures. It does not
	// require those already admitted transactions to remain pending or uncommitted.
	q.mu.Lock()
	receipt, known := r.session.receipts[frame.CorpusFrameID]
	if known {
		if ctx.Err() != nil || q.closed || q.reconciling || q.stamp != r.stamp || s.ctx.Err() != nil {
			q.mu.Unlock()
			return ResumeIngressAck{}, ErrIngressSnapshotChanged
		}
		r.releaseLocked()
		ack := s.resumeReceiptAck(q, r.session, receipt, true)
		q.mu.Unlock()
		return ack, nil
	}
	q.mu.Unlock()
	snapshot, err := s.chain.ReadPerformanceCanonicalAccounts(ctx, addresses)
	if errors.Is(err, internal.ErrPerformanceCanonicalNotApplied) {
		return ResumeIngressAck{}, ErrIngressSnapshotChanged
	}
	if err != nil {
		return ResumeIngressAck{}, err
	}
	if snapshot.Root == (types.Hash{}) || snapshot.AppliedRoot != snapshot.Root {
		return ResumeIngressAck{}, errors.New("sealed resume actual state root differs from canonical header")
	}
	for _, address := range addresses[1:] {
		a := snapshot.Accounts[address]
		if a.CodeHash != (types.Hash{}) && a.CodeHash != crypto.Keccak256Hash(nil) {
			return ResumeIngressAck{}, errors.New("sealed resume new recipient is not EOA")
		}
	}
	return s.publishResumeIngress(ctx, q, frame, r, snapshot, verified)
}

func (s *Server) publishResumeIngress(ctx context.Context, q *strictIngressQueue, frame CorpusFrame, r *resumeReservation, snapshot internal.PerformanceCanonicalAccounts, verified []ingressVerified) (ResumeIngressAck, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ResumeIngressAck{}, err
	}
	if q.closed || s.ctx.Err() != nil || q.reconciling || q.stamp != r.stamp || snapshot.Hash != r.stamp.hash || snapshot.Number != r.stamp.number || q.resume != r.session {
		return ResumeIngressAck{}, ErrIngressSnapshotChanged
	}
	if receipt, known := r.session.receipts[frame.CorpusFrameID]; known {
		r.releaseLocked()
		return s.resumeReceiptAck(q, r.session, receipt, true), nil
	}
	if r.session.window != nil {
		return s.publishBoundedResumeLocked(q, frame, r, snapshot, verified)
	}
	sender := r.session.plan.Senders[r.frame.SenderIndex]
	a, exists := snapshot.Accounts[sender.Address]
	if !exists || !snapshot.Exists[sender.Address] || a.Nonce != sender.Nonce || a.CodeHash != crypto.Keccak256Hash(nil) {
		return ResumeIngressAck{}, errors.New("sealed resume sender canonical baseline changed; audit again")
	}
	if q.lanes[sender.Address] != nil {
		return ResumeIngressAck{}, errors.New("sealed resume sender already has an unrecorded lane")
	}
	if len(r.session.receipts) >= 4 || len(verified) != int(r.frame.NewlyAdmitted) {
		return ResumeIngressAck{}, errors.New("sealed resume publication bound differs")
	}
	cost := uint256.NewInt(uint64(len(verified)) * 21001)
	if a.Balance.Lt(cost) || (len(verified) > 0 && snapshot.NextBaseFee.Gt(uint256.NewInt(1))) {
		return ResumeIngressAck{}, errors.New("sealed resume suffix funds or base fee invalid")
	}
	// All checks precede mutation. The reservation becomes one full retained
	// original payload, even though only the suffix has live queue entries.
	if len(verified) > 0 {
		slab := &ingressSlab{payload: frame.Payload, references: len(verified)}
		lane := &ingressLane{next: sender.Nonce + uint64(len(verified)), cost: *cost, entries: make([]*ingressEntry, len(verified))}
		for i, v := range verified {
			if v.sender != sender.Address || v.nonce != sender.Nonce+uint64(i) {
				return ResumeIngressAck{}, errors.New("sealed resume suffix continuity differs")
			}
			lane.entries[i] = &ingressEntry{slab: slab, start: v.start, end: v.end, nonce: v.nonce, hash: v.hash, cost: v.cost, gasPrice: v.gasPrice}
		}
		q.lanes[sender.Address] = lane
		q.pending += len(verified)
		q.bytes += len(frame.Payload)
		q.admitted += uint64(len(verified))
	}
	receipt := resumeFrameReceipt{skipped: int(r.frame.CanonicalSkipped), admitted: len(verified)}
	r.session.receipts[frame.CorpusFrameID] = receipt
	r.session.canonicalSkipped += uint64(receipt.skipped)
	r.releaseLocked()
	return s.resumeReceiptAck(q, r.session, receipt, false), nil
}
