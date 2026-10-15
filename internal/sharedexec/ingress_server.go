package sharedexec

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

type strictIngressListener struct {
	net.Listener
	owner *Server
}

// ListenStrictIngressUnix opens a distinct private endpoint. It requires the
// startup-only strict queue option; it does not enable producer consumption.
func (s *Server) ListenStrictIngressUnix(path string) (net.Listener, error) {
	s.mu.Lock()
	q, closed := s.ingress, s.closed
	s.mu.Unlock()
	if closed || q == nil {
		return nil, errors.New("strict ingress disabled or closed")
	}
	q.mu.Lock()
	if q.closed || q.transportListening {
		q.mu.Unlock()
		return nil, errors.New("strict ingress already listening or closed")
	}
	q.transportListening = true
	q.mu.Unlock()
	listener, err := listenPrivateExecutionUnix(path)
	if err != nil {
		q.mu.Lock()
		q.transportListening = false
		q.mu.Unlock()
		return nil, err
	}
	context.AfterFunc(s.ctx, func() { _ = listener.Close() })
	if err := s.ctx.Err(); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return &strictIngressListener{Listener: listener, owner: s}, nil
}

// ServeStrictIngress bounds live connections and closes their IO before Close
// waits for registered work. Each connection handles one frame at a time.
func (s *Server) ServeStrictIngress(listener net.Listener) error {
	owned, ok := listener.(*strictIngressListener)
	if !ok || owned.owner != s {
		return errors.New("strict ingress requires its private owned listener")
	}
	s.mu.Lock()
	q := s.ingress
	if s.closed || q == nil {
		s.mu.Unlock()
		return errors.New("strict ingress closed")
	}
	q.mu.Lock()
	if q.transportServing {
		q.mu.Unlock()
		s.mu.Unlock()
		return errors.New("strict ingress already serving")
	}
	q.transportServing = true
	q.mu.Unlock()
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	var liveMu sync.Mutex
	live := make(map[net.Conn]struct{})
	closeAll := func() {
		_ = listener.Close()
		liveMu.Lock()
		defer liveMu.Unlock()
		for conn := range live {
			_ = conn.Close()
		}
	}
	stop := context.AfterFunc(s.ctx, closeAll)
	defer stop()
	defer closeAll()
	slots := make(chan struct{}, CorpusMaxShards)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if s.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			<-slots
			_ = conn.Close()
			continue
		}
		s.wg.Add(1)
		liveMu.Lock()
		live[conn] = struct{}{}
		liveMu.Unlock()
		s.mu.Unlock()
		go func(conn net.Conn) {
			defer s.wg.Done()
			defer func() { _ = conn.Close(); liveMu.Lock(); delete(live, conn); liveMu.Unlock(); <-slots }()
			_ = s.serveStrictIngressConnection(q, conn)
		}(conn)
	}
}

func (s *Server) serveStrictIngressConnection(q *strictIngressQueue, conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	var magic [4]byte
	if _, err := io.ReadFull(conn, magic[:]); err != nil {
		return err
	}
	if magic != [4]byte{'G', 'S', 'I', '1'} {
		return errors.New("unsupported strict ingress protocol")
	}
	var hello ingressHello
	if err := readIngressMetadata(conn, &hello); err != nil {
		return err
	}
	if hello.Identity != s.identity || hello.ManifestHash != q.manifestHash || hello.Shard >= q.descriptor.ShardCount {
		err := errors.New("strict ingress hello identity/manifest/shard mismatch")
		_ = writeIngressMetadata(conn, s.ingressReply(q, "rejected", CorpusFrame{}, StrictIngressAck{}, err))
		return err
	}
	if err := writeIngressMetadata(conn, s.ingressReply(q, "welcome", CorpusFrame{}, StrictIngressAck{}, nil)); err != nil {
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
			return errors.New("strict ingress frame shard changed")
		}
		reservation, known, err := s.reserveStrictIngress(q, q.epoch, frame, size)
		if err != nil {
			kind := "rejected"
			if errors.Is(err, ErrIngressCreditsExhausted) || errors.Is(err, ErrIngressSnapshotChanged) {
				kind = "busy"
			}
			if writeErr := writeIngressMetadata(conn, s.ingressReply(q, kind, frame, StrictIngressAck{}, err)); writeErr != nil {
				return writeErr
			}
			if kind == "busy" {
				continue
			}
			return err
		}
		if known != nil {
			if err := writeIngressMetadata(conn, s.ingressReply(q, "duplicate", frame, *known, nil)); err != nil {
				return err
			}
			continue
		}
		ack, err := s.receiveGrantedIngress(q, conn, frame, size, reservation)
		if err != nil {
			_ = writeIngressMetadata(conn, s.ingressReply(q, "rejected", frame, StrictIngressAck{}, err))
			return err
		}
		if err := writeIngressMetadata(conn, s.ingressReply(q, "admitted", frame, ack, nil)); err != nil {
			return err
		}
	}
}

func (s *Server) receiveGrantedIngress(q *strictIngressQueue, conn net.Conn, frame CorpusFrame, size int, reservation *ingressReservation) (StrictIngressAck, error) {
	defer reservation.release()
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	if err := writeIngressMetadata(conn, s.ingressReply(q, "grant", frame, StrictIngressAck{}, nil)); err != nil {
		return StrictIngressAck{}, err
	}
	frame.Payload = make([]byte, size)
	if _, err := io.ReadFull(conn, frame.Payload); err != nil {
		return StrictIngressAck{}, err
	}
	return s.verifyReservedIngress(ctx, q, frame, reservation)
}
