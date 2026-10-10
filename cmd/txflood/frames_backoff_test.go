package main

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
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

// TestRunSequentialPerPeer: a peer answering Busy for frame 1 must not see
// frame 2 (or later) before frame 1 is accepted, even with a wide -conc.
func TestRunSequentialPerPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	gw, _ := crypto.GenerateKey()
	var mu sync.Mutex
	var log []uint64 // seq of every attempt, in arrival order
	busyLeft := 5
	accepted := map[uint64]bool{}
	var violation string
	fr := &frameRun{peers: []string{ln.Addr().String()}, key: gw, conc: 8, total: 6 * frame.MaxTxs,
		rawAt: func(i int64) []byte { return []byte{0xEE, byte(i), byte(i >> 8)} }}
	fr.sleep = func(time.Duration) {}
	fr.sendFn = func(_ net.Conn, raw, buf []byte) (byte, []byte, error) {
		h, err := frame.ParseHeader(raw[:frame.HeaderSize])
		if err != nil {
			return 0, buf, err
		}
		mu.Lock()
		defer mu.Unlock()
		log = append(log, h.Seq)
		if h.Seq > 0 && !accepted[h.Seq-1] {
			violation = fmt.Sprintf("seq %d sent before %d accepted", h.Seq, h.Seq-1)
		}
		if h.Seq == 1 && busyLeft > 0 {
			busyLeft--
			return ingest.FrameBusy, buf, nil
		}
		accepted[h.Seq] = true
		return ingest.FrameAccepted, buf, nil
	}
	res := fr.run(false)
	if violation != "" {
		t.Fatal(violation, log)
	}
	if res.Frames != 6 || res.Failed != 0 || res.Busy != 5 || fr.busyRetries() != 5 {
		t.Fatalf("result %s log %v", res, log)
	}
	if len(log) != 11 {
		t.Fatalf("attempts %v", log)
	}
}
