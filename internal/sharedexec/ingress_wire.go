package sharedexec

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"

	"github.com/n42blockchain/N42/common/types"
)

const maxIngressMetadata = 4096

var ErrIngressUnknownOutcome = errors.New("strict ingress outcome unknown; stop and reconcile, do not replay")

type ingressHello struct {
	Identity     Identity
	ManifestHash types.Hash
	Shard        uint16
}

type ingressReply struct {
	Kind         string
	Identity     Identity
	ManifestHash types.Hash
	Epoch        [16]byte
	FrameID      CorpusFrameID
	PayloadHash  types.Hash
	Ack          StrictIngressAck
	Error        string
	RetryReason  string `json:",omitempty"`
}

const ingressRetrySnapshot = "canonical_snapshot_changed"

func writeIngressBytes(w io.Writer, raw []byte) error {
	for len(raw) > 0 {
		n, err := w.Write(raw)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		raw = raw[n:]
	}
	return nil
}

func writeIngressMetadata(w io.Writer, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) == 0 || len(raw) > maxIngressMetadata {
		return errors.New("strict ingress metadata exceeds limit")
	}
	packet := make([]byte, 4+len(raw))
	binary.LittleEndian.PutUint32(packet[:4], uint32(len(raw)))
	copy(packet[4:], raw)
	return writeIngressBytes(w, packet)
}

func readIngressMetadata(r io.Reader, value any) error {
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return err
	}
	size := binary.LittleEndian.Uint32(prefix[:])
	if size == 0 || size > maxIngressMetadata {
		return errors.New("strict ingress metadata length invalid")
	}
	raw := make([]byte, int(size))
	if _, err := io.ReadFull(r, raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("strict ingress metadata has trailing values")
	}
	return nil
}

func (s *Server) ingressReply(q *strictIngressQueue, kind string, frame CorpusFrame, ack StrictIngressAck, err error) ingressReply {
	if ack.Status.Identity != (s.identity) {
		ack.Status, _ = s.StrictIngressStatus()
	}
	reply := ingressReply{Kind: kind, Identity: s.identity, ManifestHash: q.manifestHash, Epoch: q.epoch, FrameID: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, Ack: ack}
	if kind == "retry" && errors.Is(err, ErrIngressSnapshotChanged) {
		reply.RetryReason = ingressRetrySnapshot
	}
	if err != nil {
		reply.Error = err.Error()
		if len(reply.Error) > 256 {
			reply.Error = reply.Error[:256]
		}
	}
	return reply
}
