package main

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestTxfloodRawBytes(t *testing.T) {
	if got := rawBytes("0x010203"); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("rawBytes = %v", got)
	}
	if got := rawBytes("not-hex"); got != nil {
		t.Fatalf("rawBytes on invalid input = %v, want nil", got)
	}
}

// txfloodHintListener starts a loopback listener that accepts one connection,
// reads one framed batch, and replies with a 4-byte ack -- just enough wire
// protocol for hintStreamer.run to exercise its success path.
func txfloodHintListener(t *testing.T) (addr string, received chan int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	received = make(chan int, 4)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var n [4]byte
		if _, err := readFullHelper(conn, n[:]); err != nil {
			return
		}
		count := binary.LittleEndian.Uint32(n[:])
		received <- int(count)
		conn.Write([]byte{0, 0, 0, 1})
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String(), received
}

func readFullHelper(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func TestTxfloodHintStreamerOfferAndRun(t *testing.T) {
	addr, received := txfloodHintListener(t)

	h := newHintStreamer([]string{addr})
	h.offer([][]byte{{0xde, 0xad}, {0xbe, 0xef}})

	select {
	case count := <-received:
		if count != 2 {
			t.Fatalf("server saw %d transactions, want 2", count)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for hint streamer to deliver batch")
	}

	// Give run() a moment to process the ack before asserting counters.
	deadline := time.Now().Add(1 * time.Second)
	for h.sent.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.sent.Load() != 2 {
		t.Fatalf("sent = %d, want 2", h.sent.Load())
	}
	if h.dropped.Load() != 0 {
		t.Fatalf("dropped = %d, want 0", h.dropped.Load())
	}
}

func TestTxfloodHintStreamerDialFailure(t *testing.T) {
	// Port 0 after listener close is not dialable; use a closed listener's
	// former address, which should refuse the connection quickly.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	h := newHintStreamer([]string{addr})
	h.offer([][]byte{{0x01}})

	deadline := time.Now().Add(2 * time.Second)
	for h.errs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.errs.Load() == 0 {
		t.Fatal("expected a dial error to be recorded")
	}
	if h.dropped.Load() == 0 {
		t.Fatal("expected the offered batch to be dropped on dial failure")
	}
}
