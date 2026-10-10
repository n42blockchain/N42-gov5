package main

import (
	"net"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/ingest"
)

// TestSendToBusyBackoff: a peer answering Busy 8 times then Accepted gets the
// same frame resent with 5 ms doubling to 200 ms, and the retries are counted
// on that peer only.
func TestSendToBusyBackoff(t *testing.T) {
	fr := &frameRun{peers: []string{"a", "b"}}
	fr.run(false) // sizes peerBusy (total 0: no work)
	var sleeps []time.Duration
	fr.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	calls := 0
	fr.sendFn = func(_ net.Conn, _, buf []byte) (byte, []byte, error) {
		calls++
		if calls <= 8 {
			return ingest.FrameBusy, buf, nil
		}
		return ingest.FrameAccepted, buf, nil
	}
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	conn := c1
	ok, _ := fr.sendTo(&conn, 1, 7, []byte("x"), 1, nil)
	if !ok || calls != 9 {
		t.Fatalf("ok=%v calls=%d", ok, calls)
	}
	want := []time.Duration{5, 10, 20, 40, 80, 160, 200, 200}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps %v", sleeps)
	}
	for i, w := range want {
		if sleeps[i] != w*time.Millisecond {
			t.Fatalf("sleep %d = %v, want %v", i, sleeps[i], w*time.Millisecond)
		}
	}
	if fr.peerBusy[1].Load() != 8 || fr.peerBusy[0].Load() != 0 || fr.busyRetries() != 8 || fr.busy.Load() != 8 {
		t.Fatalf("busy counters: %s", fr.busyByPeer())
	}
}

// TestSendToStatusFatal: status 3 is a failure, not retried.
func TestSendToStatusFatal(t *testing.T) {
	fr := &frameRun{peers: []string{"a"}}
	fr.run(false)
	calls := 0
	fr.sendFn = func(_ net.Conn, _, buf []byte) (byte, []byte, error) { calls++; return 3, buf, nil }
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	conn := c1
	if ok, _ := fr.sendTo(&conn, 0, 1, []byte("x"), 1, nil); ok || calls != 1 {
		t.Fatalf("ok=%v calls=%d", ok, calls)
	}
}
