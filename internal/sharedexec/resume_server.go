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
		var trace *resumeFrameTrace
		if s.resumeDiagnostics != nil {
			trace = &resumeFrameTrace{started: time.Now()}
			reservation.trace = trace
		}
		s.resumeDiagnostics.frame(true)
		ack, err := s.receiveGrantedResume(q, conn, frame, size, reservation)
		if err != nil {
			kind := "rejected"
			if errors.Is(err, ErrIngressSnapshotChanged) {
				kind = "retry"
				if trace != nil {
					trace.outcome = 2
				}
			} else {
				if trace != nil {
					trace.outcome = 3
				}
			}
			writeErr := writeIngressMetadata(conn, s.resumeReply(q, kind, frame, ResumeIngressAck{}, err))
			s.resumeDiagnostics.frame(false)
			s.resumeDiagnostics.merge(trace)
			if writeErr != nil {
				return writeErr
			}
			if kind == "retry" {
				continue
			}
			return err
		}
		writeStart := time.Now()
		writeErr := writeIngressMetadata(conn, s.resumeReply(q, "admitted", frame, ack, nil))
		trace.add(10, time.Since(writeStart))
		if writeErr != nil {
			if trace != nil {
				trace.outcome = 4
			}
			s.resumeDiagnostics.frame(false)
			s.resumeDiagnostics.merge(trace)
			return writeErr
		}
		s.resumeDiagnostics.frame(false)
		if ack.Duplicate {
			if trace != nil {
				trace.outcome = 1
			}
		} else {
			if trace != nil {
				trace.add(11, time.Since(trace.started))
			}
		}
		s.resumeDiagnostics.merge(trace)
	}
}
func (s *Server) receiveGrantedResume(q *strictIngressQueue, conn net.Conn, frame CorpusFrame, size int, reservation *resumeReservation) (ResumeIngressAck, error) {
	defer reservation.release()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	grantStart := time.Now()
	grantErr := writeIngressMetadata(conn, s.resumeReply(q, "grant", frame, ResumeIngressAck{}, nil))
	reservation.trace.add(1, time.Since(grantStart))
	if grantErr != nil {
		return ResumeIngressAck{}, grantErr
	}
	frame.Payload = make([]byte, size)
	readStart := time.Now()
	_, readErr := io.ReadFull(conn, frame.Payload)
	reservation.trace.add(2, time.Since(readStart))
	if readErr != nil {
		return ResumeIngressAck{}, readErr
	}
	return s.verifyReservedResume(ctx, q, frame, reservation)
}
