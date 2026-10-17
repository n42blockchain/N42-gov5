package sharedexec

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/n42blockchain/N42/common/types"
)

// ResumeIngressClient never reconnects or replays an ambiguous original frame.
// It pins the locally chosen plan and epoch before transmitting the first frame.
type ResumeIngressClient struct {
	base   *StrictIngressClient
	planID types.Hash
}

func DialResumeIngress(ctx context.Context, path string, identity Identity, manifest, planID types.Hash, epoch [16]byte, shard uint16) (*ResumeIngressClient, error) {
	if ctx == nil {
		return nil, errors.New("active sealed resume dial context required")
	}
	if !filepath.IsAbs(path) || manifest == (types.Hash{}) || planID == (types.Hash{}) || epoch == ([16]byte{}) || shard >= CorpusMaxShards {
		return nil, errors.New("invalid sealed resume endpoint/binding")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	base := &StrictIngressClient{conn: conn, identity: identity, manifest: manifest, epoch: epoch, shard: shard}
	if err := base.deadline(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	fail := func(err error) (*ResumeIngressClient, error) { _ = conn.Close(); return nil, err }
	if err := writeIngressBytes(conn, []byte("GSR1")); err != nil {
		return fail(err)
	}
	if err := writeIngressMetadata(conn, resumeHello{Identity: identity, ManifestHash: manifest, PlanID: planID, Epoch: epoch, Shard: shard}); err != nil {
		return fail(err)
	}
	var reply resumeReply
	if err := readResumeMetadata(conn, &reply); err != nil {
		return fail(err)
	}
	if reply.Kind != "welcome" || reply.Identity != identity || reply.ManifestHash != manifest || reply.PlanID != planID || reply.Epoch != epoch || reply.Ack.PlanID != planID || reply.Ack.Status.Identity != identity || reply.Ack.Status.ManifestHash != manifest || reply.Ack.Status.Epoch != epoch || !zeroResumeCounts(reply.Ack) || reply.Error != "" || reply.RetryReason != "" {
		return fail(errors.New("sealed resume welcome binding differs"))
	}
	return &ResumeIngressClient{base: base, planID: planID}, nil
}
func zeroResumeCounts(ack ResumeIngressAck) bool {
	return ack.CanonicalSkipped == 0 && ack.NewlyAdmitted == 0 && ack.PriorAdmission == 0 && !ack.Duplicate
}
func (c *ResumeIngressClient) Close() error       { return c.base.Close() }
func (c *ResumeIngressClient) Epoch() [16]byte    { return c.base.Epoch() }
func (c *ResumeIngressClient) PlanID() types.Hash { return c.planID }

func (c *ResumeIngressClient) Admit(ctx context.Context, frame CorpusFrame, selected ResumeFrame) (ResumeIngressAck, error) {
	if ctx == nil {
		return ResumeIngressAck{}, errors.New("active sealed resume admission context required")
	}
	b := c.base
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.broken {
		return ResumeIngressAck{}, ErrIngressUnknownOutcome
	}
	if frame.Shard != b.shard || frame.CorpusFrameID != selected.ID || frame.PayloadHash != selected.PayloadHash || len(frame.Payload) != int(selected.PayloadBytes) || frame.Transactions != 500 || uint32(selected.CanonicalSkipped)+uint32(selected.NewlyAdmitted) != 500 {
		return ResumeIngressAck{}, errors.New("sealed resume client original frame differs")
	}
	header, err := frame.Header()
	if err != nil {
		return ResumeIngressAck{}, err
	}
	if err := b.deadline(ctx); err != nil {
		return ResumeIngressAck{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = b.conn.Close() })
	defer stop()
	phase := "write-header"
	unknown := func(err error) (ResumeIngressAck, error) {
		b.broken = true
		_ = b.conn.Close()
		return ResumeIngressAck{}, fmt.Errorf("%w: sealed resume %s: %v (context: %v)", ErrIngressUnknownOutcome, phase, err, ctx.Err())
	}
	if err := writeIngressBytes(b.conn, header[:]); err != nil {
		return unknown(err)
	}
	read := func() (resumeReply, error) {
		var reply resumeReply
		if err := readResumeMetadata(b.conn, &reply); err != nil {
			return reply, err
		}
		if reply.Identity != b.identity || reply.ManifestHash != b.manifest || reply.PlanID != c.planID || reply.Epoch != b.epoch || reply.FrameID != frame.CorpusFrameID || reply.PayloadHash != frame.PayloadHash || reply.Ack.PlanID != c.planID || reply.Ack.Status.Identity != b.identity || reply.Ack.Status.ManifestHash != b.manifest || reply.Ack.Status.Epoch != b.epoch {
			return reply, errors.New("sealed resume ACK binding differs")
		}
		if reply.Kind != "retry" && reply.RetryReason != "" {
			return reply, errors.New("unexpected sealed resume retry reason")
		}
		if (reply.Kind == "grant" || reply.Kind == "admitted") && reply.Error != "" {
			return reply, errors.New("sealed resume successful ACK carried an error")
		}
		return reply, nil
	}
	knownZero := func(reply resumeReply) (ResumeIngressAck, error) {
		if !zeroResumeCounts(reply.Ack) {
			return unknown(errors.New("sealed resume known-zero ACK carried counts"))
		}
		switch reply.Kind {
		case "retry":
			if reply.RetryReason != ingressRetrySnapshot {
				return unknown(errors.New("unknown sealed resume retry reason"))
			}
			return reply.Ack, ErrIngressSnapshotChanged
		case "busy":
			return reply.Ack, ErrIngressCreditsExhausted
		case "rejected":
			b.broken = true
			_ = b.conn.Close()
			return reply.Ack, fmt.Errorf("sealed resume rejected: %s", reply.Error)
		}
		return unknown(errors.New("unexpected sealed resume ACK"))
	}
	phase = "read-header-ack"
	reply, err := read()
	if err != nil {
		return unknown(err)
	}
	if reply.Kind != "grant" {
		return knownZero(reply)
	}
	if !zeroResumeCounts(reply.Ack) {
		return unknown(errors.New("sealed resume grant carried admission"))
	}
	phase = "write-payload"
	if err := writeIngressBytes(b.conn, frame.Payload); err != nil {
		return unknown(err)
	}
	phase = "read-final-ack"
	reply, err = read()
	if err != nil {
		return unknown(err)
	}
	if reply.Kind == "retry" || reply.Kind == "rejected" {
		return knownZero(reply)
	}
	ack := reply.Ack
	if reply.Kind != "admitted" || ack.CanonicalSkipped != int(selected.CanonicalSkipped) || ack.PriorAdmission != int(selected.NewlyAdmitted) || (!ack.Duplicate && ack.NewlyAdmitted != int(selected.NewlyAdmitted)) || (ack.Duplicate && ack.NewlyAdmitted != 0) {
		return unknown(errors.New("sealed resume final receipt counts differ"))
	}
	return ack, nil
}
