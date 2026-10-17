package sharedexec

import (
	"encoding/binary"
	"errors"
	"io"

	"github.com/n42blockchain/N42/common/types"
)

type resumeHello struct {
	Identity             Identity
	ManifestHash, PlanID types.Hash
	Epoch                [16]byte
	Shard                uint16
}
type resumeReply struct {
	Kind                 string
	Identity             Identity
	ManifestHash, PlanID types.Hash
	Epoch                [16]byte
	FrameID              CorpusFrameID
	PayloadHash          types.Hash
	Ack                  ResumeIngressAck
	Error                string
	RetryReason          string `json:",omitempty"`
}

func readResumeMetadata(r io.Reader, value any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	size := binary.LittleEndian.Uint32(prefix[:])
	if size == 0 || size > maxIngressMetadata {
		return errors.New("sealed resume metadata exceeds bounds")
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	return decodeResumePlanJSON(raw, value)
}

func (s *Server) resumeReply(q *strictIngressQueue, kind string, frame CorpusFrame, ack ResumeIngressAck, err error) resumeReply {
	q.mu.Lock()
	if ack.Status.Identity != s.identity {
		ack.Status = q.status(s.identity)
	}
	planID := q.resume.planID
	ack.PlanID = planID
	q.mu.Unlock()
	reply := resumeReply{Kind: kind, Identity: s.identity, ManifestHash: q.manifestHash, PlanID: planID, Epoch: q.epoch, FrameID: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, Ack: ack}
	if err != nil {
		reply.Error = err.Error()
	}
	if kind == "retry" {
		reply.RetryReason = ingressRetrySnapshot
	}
	return reply
}
