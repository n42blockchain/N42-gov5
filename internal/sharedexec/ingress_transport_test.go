package sharedexec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func startStrictIngressSocket(t *testing.T, s *Server) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ingress.sock")
	listener, err := s.ListenStrictIngressUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.ServeStrictIngress(listener) }()
	t.Cleanup(func() {
		s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("ingress Serve did not stop")
		}
	})
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private socket %v %v", info, err)
	}
	return path
}
func rawStrictIngress(t *testing.T, path string, s *Server, shard uint16) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	status, _ := s.StrictIngressStatus()
	if err := writeIngressBytes(conn, []byte("GSI1")); err != nil {
		t.Fatal(err)
	}
	if err := writeIngressMetadata(conn, ingressHello{Identity: s.Identity(), ManifestHash: status.ManifestHash, Shard: shard}); err != nil {
		t.Fatal(err)
	}
	var reply ingressReply
	if err := readIngressMetadata(conn, &reply); err != nil || reply.Kind != "welcome" {
		t.Fatalf("welcome %+v %v", reply, err)
	}
	return conn
}
func strictHeader(t *testing.T, conn net.Conn, frame CorpusFrame) ingressReply {
	t.Helper()
	header, err := frame.Header()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeIngressBytes(conn, header[:]); err != nil {
		t.Fatal(err)
	}
	var reply ingressReply
	if err := readIngressMetadata(conn, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func waitStrictReservations(t *testing.T, s *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, _ := s.StrictIngressStatus()
		if status.ReservedTransactions == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("reservation leak %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
}
func TestStrictIngressUnixGrantDisconnectBusyAndDuplicate(t *testing.T) {
	f := newStrictIngressFixture(t, 3, 1<<20)
	path := startStrictIngressSocket(t, f.server)
	first := rawStrictIngress(t, path, f.server, 0)
	if reply := strictHeader(t, first, f.frames[0]); reply.Kind != "grant" {
		t.Fatalf("no grant %+v", reply)
	}
	waitStrictReservations(t, f.server, 3)
	status, _ := f.server.StrictIngressStatus()
	c, err := DialStrictIngress(context.Background(), path, f.server.Identity(), status.ManifestHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Admit(context.Background(), f.frames[1]); !errors.Is(err, ErrIngressCreditsExhausted) {
		t.Fatalf("busy %v", err)
	}
	// A grant followed by a partial payload owns no lasting queue allocation.
	if err := writeIngressBytes(first, f.frames[0].Payload[:7]); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	waitStrictReservations(t, f.server, 0)
	ack, err := c.Admit(context.Background(), f.frames[1])
	if err != nil || ack.NewlyAdmitted != 3 {
		t.Fatalf("busy connection lost framing %+v %v", ack, err)
	}
	// Only the header is uploaded for this published duplicate; reading its reply
	// without sending a body proves the shortcut does not wait for payload bytes.
	duplicate := rawStrictIngress(t, path, f.server, 1)
	reply := strictHeader(t, duplicate, f.frames[1])
	if reply.Kind != "duplicate" || reply.Ack.Duplicates != 3 || reply.Ack.NewlyAdmitted != 0 {
		t.Fatalf("duplicate %+v", reply)
	}
	status, _ = f.server.StrictIngressStatus()
	if status.CryptoVerified != 3 || status.NewlyAdmitted != 3 || status.ReservedEncodedBytes != 0 {
		t.Fatalf("duplicate reverified or charged %+v", status)
	}
}
func TestStrictIngressUnixConnectionCapAndClose(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	path := startStrictIngressSocket(t, f.server)
	conns := make([]net.Conn, 0, 64)
	// Sequential welcome ACKs establish that each of the 64 slots is occupied.
	for i := 0; i < 64; i++ {
		conns = append(conns, rawStrictIngress(t, path, f.server, 0))
	}
	extra, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	_ = extra.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := extra.Read(one[:]); err == nil {
		t.Fatal("65th connection was not rejected")
	} else if timed, ok := err.(net.Error); ok && timed.Timeout() {
		t.Fatal("65th connection stayed open")
	}
	// All first 63 handlers wait in header IO; the last waits in body IO.
	if reply := strictHeader(t, conns[63], f.frames[0]); reply.Kind != "grant" {
		t.Fatal(reply)
	}
	waitStrictReservations(t, f.server, 3)
	done := make(chan struct{})
	go func() { f.server.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked in ingress IO")
	}
	status, _ := f.server.StrictIngressStatus()
	if status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 || status.PendingTransactions != 0 {
		t.Fatalf("Close retained credits %+v", status)
	}
}
func TestStrictIngressMetadataBoundsAndDefaultDisabled(t *testing.T) {
	var hello ingressHello
	var prefix [4]byte
	binary.LittleEndian.PutUint32(prefix[:], 4097)
	if err := readIngressMetadata(bytes.NewReader(prefix[:]), &hello); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	for _, raw := range []string{`{"Unexpected":1}`, `{} {}`} {
		var packet bytes.Buffer
		binary.LittleEndian.PutUint32(prefix[:], uint32(len(raw)))
		packet.Write(prefix[:])
		packet.WriteString(raw)
		if err := readIngressMetadata(&packet, &hello); err == nil {
			t.Fatal("unknown/trailing metadata accepted")
		}
	}
	chain, _, _, template, _, _ := unsignedBuildFixtureWithSet(t, 7, nil)
	s, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ListenStrictIngressUnix(filepath.Join(t.TempDir(), "disabled.sock")); err == nil {
		t.Fatal("default enabled transport")
	}
	if err := s.ServeStrictIngress(nil); err == nil {
		t.Fatal("unowned listener accepted")
	}
	rawRequest := buildRequest(t, s.Identity(), template)
	if _, err := s.BuildFromQueue(context.Background(), QueueBuildRequest{Identity: s.Identity(), Epoch: [16]byte{1}, PreparedHash: rawRequest.TemplateHash, PreparedRLP: rawRequest.TemplateRLP, MaxTransactions: 6, MaxEncodedBytes: 1 << 20}); err == nil {
		t.Fatal("default owner silently enabled queue build")
	}
}
func TestStrictIngressClientUnknownAckNeverReplays(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	path := startStrictIngressSocket(t, f.server)
	status, _ := f.server.StrictIngressStatus()
	bad := status.ManifestHash
	bad[0] ^= 1
	if c, err := DialStrictIngress(context.Background(), path, f.server.Identity(), bad, 0); err == nil {
		c.Close()
		t.Fatal("wrong manifest accepted")
	}
	// The peer closes after consuming the header, before any grant ACK. Unlike
	// a local pre-write deadline failure, this is an ambiguous protocol outcome.
	local, peer := net.Pipe()
	c := &StrictIngressClient{conn: local, identity: f.server.Identity(), manifest: status.ManifestHash, epoch: status.Epoch, shard: 0}
	defer c.Close()
	go func() { defer peer.Close(); var header [CorpusWireHeaderSize]byte; _, _ = io.ReadFull(peer, header[:]) }()
	if _, err := c.Admit(context.Background(), f.frames[0]); !errors.Is(err, ErrIngressUnknownOutcome) {
		t.Fatalf("lost ACK outcome %v", err)
	}
	if _, err := c.Admit(context.Background(), f.frames[0]); !errors.Is(err, ErrIngressUnknownOutcome) {
		t.Fatalf("broken client replay %v", err)
	}
	// Truncated metadata must not decode as a valid grant.
	if err := readIngressMetadata(bytes.NewReader([]byte{5, 0, 0, 0, 1}), &ingressReply{}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}

// net.Pipe has no write buffer: not reading the final ACK deterministically
// blocks the handler at ACK IO, unlike a small ACK buffered by a Unix socket.
type ingressPipeListener struct {
	conn      net.Conn
	delivered bool
	closed    chan struct{}
	once      sync.Once
}

func (l *ingressPipeListener) Accept() (net.Conn, error) {
	if !l.delivered {
		l.delivered = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *ingressPipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *ingressPipeListener) Addr() net.Addr { return &net.UnixAddr{Name: "test-pipe", Net: "unix"} }
func TestStrictIngressCloseBlockedFinalAck(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	client, server := net.Pipe()
	defer client.Close()
	listener := &strictIngressListener{Listener: &ingressPipeListener{conn: server, closed: make(chan struct{})}, owner: f.server}
	served := make(chan error, 1)
	go func() { served <- f.server.ServeStrictIngress(listener) }()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	status, _ := f.server.StrictIngressStatus()
	if err := writeIngressBytes(client, []byte("GSI1")); err != nil {
		t.Fatal(err)
	}
	if err := writeIngressMetadata(client, ingressHello{Identity: f.server.Identity(), ManifestHash: status.ManifestHash, Shard: 0}); err != nil {
		t.Fatal(err)
	}
	var reply ingressReply
	if err := readIngressMetadata(client, &reply); err != nil {
		t.Fatal(err)
	}
	if reply := strictHeader(t, client, f.frames[0]); reply.Kind != "grant" {
		t.Fatal(reply)
	}
	if err := writeIngressBytes(client, f.frames[0].Payload); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		status, _ = f.server.StrictIngressStatus()
		if status.NewlyAdmitted == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("body was not published %+v", status)
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan struct{})
	go func() { f.server.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on unread final ACK")
	}
	select {
	case err := <-served:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve retained blocked ACK")
	}
	status, _ = f.server.StrictIngressStatus()
	if status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 || status.RetainedEncodedBytes != 0 {
		t.Fatalf("ACK cancellation leaked credits %+v", status)
	}
}

func TestStrictIngressUnixConcurrentFrameConflictPublishesOnce(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	path := startStrictIngressSocket(t, f.server)
	frames := f.frames
	frames[1].CorpusFrameID = frames[0].CorpusFrameID
	conns := []net.Conn{rawStrictIngress(t, path, f.server, 0), rawStrictIngress(t, path, f.server, 0)}
	for i, conn := range conns {
		if reply := strictHeader(t, conn, frames[i]); reply.Kind != "grant" {
			t.Fatal(reply)
		}
	}
	waitStrictReservations(t, f.server, 6)
	type outcome struct {
		reply ingressReply
		err   error
	}
	results := make(chan outcome, 2)
	for i, conn := range conns {
		go func(i int, conn net.Conn) {
			var reply ingressReply
			err := writeIngressBytes(conn, frames[i].Payload)
			if err == nil {
				err = readIngressMetadata(conn, &reply)
			}
			results <- outcome{reply, err}
		}(i, conn)
	}
	admitted, rejected := 0, 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		switch result.reply.Kind {
		case "admitted":
			admitted++
			if result.reply.Ack.NewlyAdmitted != 3 {
				t.Fatal(result.reply)
			}
		case "rejected":
			rejected++
			if result.reply.Ack.NewlyAdmitted != 0 {
				t.Fatal(result.reply)
			}
		default:
			t.Fatal(result.reply)
		}
	}
	if admitted != 1 || rejected != 1 {
		t.Fatalf("admitted=%d rejected=%d", admitted, rejected)
	}
	waitStrictReservations(t, f.server, 0)
	status, _ := f.server.StrictIngressStatus()
	if status.PendingTransactions != 3 || status.NewlyAdmitted != 3 {
		t.Fatalf("conflicting frame published twice %+v", status)
	}
}

type ingressDropAckListener struct{ net.Listener }
type ingressDropAckConn struct{ net.Conn }

func (l ingressDropAckListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return ingressDropAckConn{c}, nil
}
func (c ingressDropAckConn) Write(raw []byte) (int, error) {
	if bytes.Contains(raw, []byte(`"Kind":"admitted"`)) {
		_ = c.Conn.Close()
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(raw)
}
func TestStrictIngressClientLostPublishedAckIsUnknown(t *testing.T) {
	f := newStrictIngressFixture(t, 6, 1<<20)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "drop-ack.sock")
	listener, err := f.server.ListenStrictIngressUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	// Drop the final ACK after the real server has published the frame. Hello,
	// grant and raw body all cross the actual private Unix socket unchanged.
	owned := listener.(*strictIngressListener)
	owned.Listener = ingressDropAckListener{owned.Listener}
	done := make(chan error, 1)
	go func() { done <- f.server.ServeStrictIngress(owned) }()
	t.Cleanup(func() {
		f.server.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("drop ACK server did not stop")
		}
	})
	status, _ := f.server.StrictIngressStatus()
	c, err := DialStrictIngress(context.Background(), path, f.server.Identity(), status.ManifestHash, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for range 2 {
		if _, err := c.Admit(context.Background(), f.frames[0]); !errors.Is(err, ErrIngressUnknownOutcome) {
			t.Fatalf("missing published ACK %v", err)
		}
	}
	status, _ = f.server.StrictIngressStatus()
	if status.NewlyAdmitted != 3 || status.PendingTransactions != 3 || status.CryptoVerified != 3 || status.ReservedTransactions != 0 {
		t.Fatalf("unknown outcome replayed or reverted %+v", status)
	}
}
