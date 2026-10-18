package sharedexec

import (
	"context"
	"errors"
	"io"
	"net"
	"time"
)

// Called only after the common owned listener consumed the exact GSR1 magic.
func (s *Server) serveResumeIngressConnection(q *strictIngressQueue, conn net.Conn) error {
	if q.resume == nil {
		return errors.New("sealed resume protocol disabled")
	}
	var hello resumeHello
	if err := readResumeMetadata(conn, &hello); err != nil {
		return err
	}
	if hello.Identity != s.identity || hello.ManifestHash != q.manifestHash || hello.PlanID != q.resume.planID || hello.Epoch != q.epoch || hello.Shard >= q.descriptor.ShardCount {
		return errors.New("sealed resume hello identity/plan/epoch differs")
	}
	selected := false
	if q.resume.window != nil {
		selected = q.resume.window.shards[hello.Shard]
	} else {
		for _, frame := range q.resume.plan.Frames {
			if frame.ID.Shard == hello.Shard {
				selected = true
			}
		}
	}
	if !selected {
		return errors.New("sealed resume hello shard outside whitelist")
	}
	if err := writeIngressMetadata(conn, s.resumeReply(q, "welcome", CorpusFrame{}, ResumeIngressAck{}, nil)); err != nil {
		return err
	}
	for {
		if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
			return err
		}
		frame, size, err := readCorpusFrameHeader(conn)
		if err != nil {
			return err
		}
		if frame.Shard != hello.Shard {
			return errors.New("sealed resume connection shard changed")
		}
		reservation, err := s.reserveResumeIngress(q, hello.PlanID, hello.Epoch, frame, size)
		if err != nil {
			kind := "rejected"
			if errors.Is(err, ErrIngressCreditsExhausted) {
				kind = "busy"
			} else if errors.Is(err, ErrIngressSnapshotChanged) {
				kind = "retry"
			}
			if writeErr := writeIngressMetadata(conn, s.resumeReply(q, kind, frame, ResumeIngressAck{}, err)); writeErr != nil {
				return writeErr
			}
			if kind == "busy" || kind == "retry" {
				continue
			}
			return err
		}
		// Even an already published receipt receives the complete original payload
		// under byte credit and verifies every signature before a duplicate ACK.
		ack, err := s.receiveGrantedResume(q, conn, frame, size, reservation)
		if err != nil {
			kind := "rejected"
			if errors.Is(err, ErrIngressSnapshotChanged) {
				kind = "retry"
			}
			if writeErr := writeIngressMetadata(conn, s.resumeReply(q, kind, frame, ResumeIngressAck{}, err)); writeErr != nil {
				return writeErr
			}
			if kind == "retry" {
				continue
			}
			return err
		}
		if err := writeIngressMetadata(conn, s.resumeReply(q, "admitted", frame, ack, nil)); err != nil {
			return err
		}
	}
}
func (s *Server) receiveGrantedResume(q *strictIngressQueue, conn net.Conn, frame CorpusFrame, size int, reservation *resumeReservation) (ResumeIngressAck, error) {
	defer reservation.release()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if err := writeIngressMetadata(conn, s.resumeReply(q, "grant", frame, ResumeIngressAck{}, nil)); err != nil {
		return ResumeIngressAck{}, err
	}
	frame.Payload = make([]byte, size)
	if _, err := io.ReadFull(conn, frame.Payload); err != nil {
		return ResumeIngressAck{}, err
	}
	return s.verifyReservedResume(ctx, q, frame, reservation)
}
