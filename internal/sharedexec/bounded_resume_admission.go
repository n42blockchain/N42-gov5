package sharedexec

import (
	"context"
	"errors"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
)

var ErrBoundedResumeLifetimeExhausted = errors.New("bounded resume nonrefundable session limit exhausted")

// Lifetime includes in-flight reservations independently of releasable queue
// credits. Canonical commits never decrement the admitted counters.
type BoundedResumeLifetime struct {
	MaxSourceFrames       uint64
	MaxSourceTransactions uint64
	MaxSourceEncodedBytes uint64
	MaxNewTransactions    uint64
	SourceFrames          uint64
	SourceTransactions    uint64
	SourceEncodedBytes    uint64
	NewTransactions       uint64
	ReservedFrames        uint64
	ReservedTransactions  uint64
	ReservedEncodedBytes  uint64
	ReservedNew           uint64
}

func (w *boundedResumeSession) status() *BoundedResumeLifetime {
	return &BoundedResumeLifetime{MaxSourceFrames: w.plan.MaxSourceFrames, MaxSourceTransactions: w.plan.MaxSourceTransactions, MaxSourceEncodedBytes: w.plan.SourceEncodedBytes, MaxNewTransactions: w.plan.MaxNewTransactions, SourceFrames: w.admittedFrames, SourceTransactions: w.admittedTransactions, SourceEncodedBytes: w.admittedBytes, NewTransactions: w.admittedNew, ReservedFrames: w.reservedFrames, ReservedTransactions: w.reservedTransactions, ReservedEncodedBytes: w.reservedBytes, ReservedNew: w.reservedNew}
}

func boundedResumeCanReserve(used, reserved, count, cap uint64) error {
	if used > cap || count > cap-used {
		return ErrBoundedResumeLifetimeExhausted
	}
	if reserved > cap-used || count > cap-used-reserved {
		return ErrIngressCreditsExhausted
	}
	return nil
}

// Called with queue.mu held. A known receipt requires full byte queue credit
// and signature checks, but does not reserve a second unique-source budget.
func (w *boundedResumeSession) checkReservation(frame ResumeFrame, known bool) error {
	if known {
		return nil
	}
	for _, v := range [][4]uint64{
		{w.admittedFrames, w.reservedFrames, 1, w.plan.MaxSourceFrames},
		{w.admittedTransactions, w.reservedTransactions, 500, w.plan.MaxSourceTransactions},
		{w.admittedBytes, w.reservedBytes, uint64(CorpusWireHeaderSize) + uint64(frame.PayloadBytes), w.plan.SourceEncodedBytes},
		{w.admittedNew, w.reservedNew, uint64(frame.NewlyAdmitted), w.plan.MaxNewTransactions},
	} {
		if err := boundedResumeCanReserve(v[0], v[1], v[2], v[3]); err != nil {
			return err
		}
	}
	return nil
}

func (r *resumeReservation) releaseLocked() {
	r.ingressReservation.releaseLocked()
	if r.lifetimeHeld {
		w := r.session.window
		w.reservedFrames--
		w.reservedTransactions -= 500
		w.reservedBytes -= uint64(CorpusWireHeaderSize) + uint64(r.frame.PayloadBytes)
		w.reservedNew -= uint64(r.frame.NewlyAdmitted)
		r.lifetimeHeld = false
	}
}

func (r *resumeReservation) release() {
	r.queue.mu.Lock()
	defer r.queue.mu.Unlock()
	r.releaseLocked()
}

func boundedResumeOriginalFrame(f BoundedResumeFrame) ResumeFrame {
	return ResumeFrame{ID: f.ID, SenderIndex: f.SenderIndex, PayloadHash: f.PayloadHash, PayloadBytes: f.PayloadBytes, CanonicalSkipped: f.CanonicalSkipped, NewlyAdmitted: f.NewlyAdmitted}
}

func (s *Server) AdmitBoundedResumeFrame(ctx context.Context, identity Identity, planID types.Hash, epoch [16]byte, frame CorpusFrame) (ResumeIngressAck, error) {
	return s.admitResumeFrame(ctx, identity, planID, epoch, frame, true)
}

// publishBoundedResumeLocked is called only after full original signature
// verification and final canonical stamp checks, while holding queue.mu.
func (s *Server) publishBoundedResumeLocked(q *strictIngressQueue, frame CorpusFrame, r *resumeReservation, snapshot internal.PerformanceCanonicalAccounts, verified []ingressVerified) (ResumeIngressAck, error) {
	w := r.session.window
	selected, ok := w.frames[frame.CorpusFrameID]
	if !ok || !r.lifetimeHeld || uint64(len(r.session.receipts)) >= w.plan.MaxSourceFrames || len(verified) != int(selected.NewlyAdmitted) {
		return ResumeIngressAck{}, errors.New("bounded resume publication or held lifetime reservation differs")
	}
	sender := r.session.plan.Senders[selected.SenderIndex]
	a, exists := snapshot.Accounts[sender.Address]
	if !exists || !snapshot.Exists[sender.Address] || a.CodeHash != crypto.Keccak256Hash(nil) || a.Nonce < sender.Nonce || a.Nonce > w.plan.NonceEndExclusive {
		return ResumeIngressAck{}, errors.New("bounded resume sender canonical state regressed or disappeared")
	}
	newStart := selected.FirstNonce + uint64(selected.CanonicalSkipped)
	var lane *ingressLane
	if len(verified) > 0 {
		// Fixed historical skip is never enlarged using a later canonical nonce.
		if a.Nonce > newStart {
			return ResumeIngressAck{}, errors.New("bounded resume unrecorded suffix became canonical; audit again")
		}
		if existing := q.lanes[sender.Address]; existing != nil {
			lane = &ingressLane{next: existing.next, cost: existing.cost, entries: append([]*ingressEntry(nil), existing.entries...)}
		} else {
			lane = &ingressLane{next: a.Nonce}
		}
		if lane.next != newStart || snapshot.NextBaseFee.Gt(uint256.NewInt(1)) {
			return ResumeIngressAck{}, errors.New("bounded resume suffix nonce gap or fee changed")
		}
		slab := &ingressSlab{payload: frame.Payload, references: len(verified)}
		for i, v := range verified {
			if v.sender != sender.Address || v.nonce != newStart+uint64(i) || v.nonce != lane.next {
				return ResumeIngressAck{}, errors.New("bounded resume original suffix continuity differs")
			}
			if _, overflow := lane.cost.AddOverflow(&lane.cost, &v.cost); overflow || lane.cost.Gt(&a.Balance) {
				return ResumeIngressAck{}, errors.New("bounded resume canonical funds cannot cover pending suffixes")
			}
			lane.entries = append(lane.entries, &ingressEntry{slab: slab, start: v.start, end: v.end, nonce: v.nonce, hash: v.hash, cost: v.cost, gasPrice: v.gasPrice})
			lane.next++
		}
	}
	// Nothing above mutates queue state. Publish the complete suffix, immutable
	// receipt and lifetime charges atomically; only queue credits later recycle.
	if lane != nil {
		q.lanes[sender.Address] = lane
		q.pending += len(verified)
		q.bytes += len(frame.Payload)
		q.admitted += uint64(len(verified))
	}
	receipt := resumeFrameReceipt{skipped: int(selected.CanonicalSkipped), admitted: len(verified)}
	r.session.receipts[frame.CorpusFrameID] = receipt
	r.session.canonicalSkipped += uint64(receipt.skipped)
	w.admittedFrames++
	w.admittedTransactions += 500
	w.admittedBytes += uint64(CorpusWireHeaderSize) + uint64(selected.PayloadBytes)
	w.admittedNew += uint64(len(verified))
	r.releaseLocked()
	return s.resumeReceiptAck(q, r.session, receipt, false), nil
}
