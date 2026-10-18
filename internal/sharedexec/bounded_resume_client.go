package sharedexec

import (
	"context"
	"errors"
)

func (c *ResumeIngressClient) checkLifetime(ack ResumeIngressAck) error {
	l := ack.Lifetime
	p := c.boundedPlan
	if p == nil {
		if l != nil {
			return errors.New("legacy sealed resume ACK unexpectedly changed lifetime protocol")
		}
		return nil
	}
	if l == nil || l.MaxSourceFrames != p.MaxSourceFrames || l.MaxSourceTransactions != p.MaxSourceTransactions || l.MaxSourceEncodedBytes != p.SourceEncodedBytes || l.MaxNewTransactions != p.MaxNewTransactions {
		return errors.New("bounded resume ACK lifetime caps differ from the pinned plan")
	}
	for _, v := range [][3]uint64{
		{l.SourceFrames, l.ReservedFrames, p.MaxSourceFrames},
		{l.SourceTransactions, l.ReservedTransactions, p.MaxSourceTransactions},
		{l.SourceEncodedBytes, l.ReservedEncodedBytes, p.SourceEncodedBytes},
		{l.NewTransactions, l.ReservedNew, p.MaxNewTransactions},
	} {
		if err := boundedResumeCanReserve(v[0], v[1], 0, v[2]); err != nil {
			return errors.New("bounded resume ACK exceeded nonrefundable lifetime caps")
		}
	}
	if l.SourceTransactions != l.SourceFrames*500 || l.ReservedTransactions != l.ReservedFrames*500 || l.NewTransactions > l.SourceTransactions || l.ReservedNew > l.ReservedTransactions || ack.Status.NewlyAdmitted != l.NewTransactions || ack.Status.ReservedTransactions < 0 || uint64(ack.Status.ReservedTransactions) != l.ReservedNew {
		return errors.New("bounded resume ACK lifetime and queue admission accounting differ")
	}
	return nil
}

// AdmitBounded transmits one unchanged original frame. A caller must retain its
// own shared lifetime budget; server-reported credits never authorize new input.
func (c *ResumeIngressClient) AdmitBounded(ctx context.Context, frame CorpusFrame, selected BoundedResumeFrame) (ResumeIngressAck, error) {
	p := c.boundedPlan
	if p == nil || selected.ID.CorpusID != p.CorpusID || int(selected.SenderIndex) >= len(p.Senders) || selected.FirstNonce%500 != 0 || selected.FirstNonce >= p.NonceEndExclusive || selected.Transactions != 500 {
		return ResumeIngressAck{}, errors.New("explicit bounded resume original frame required")
	}
	sender := p.Senders[selected.SenderIndex]
	var skip uint64
	if sender.Nonce > selected.FirstNonce {
		skip = sender.Nonce - selected.FirstNonce
		if skip > 500 {
			skip = 500
		}
	}
	if selected.Address != sender.Address || uint64(selected.CanonicalSkipped) != skip || uint64(selected.NewlyAdmitted) != 500-skip {
		return ResumeIngressAck{}, errors.New("bounded resume client fixed canonical prefix differs")
	}
	return c.Admit(ctx, frame, boundedResumeOriginalFrame(selected))
}
