package main

import (
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/ingest"
)

// frameRun is the -frames mode: the run's pre-signed transactions are packed
// frame.MaxTxs at a time into gateway-signed frames and sent over the binary
// ingest port (internal/ingest) instead of JSON-RPC. Each peer has one
// connection and is paced by the acks: one frame in flight per peer, in order.
type frameRun struct {
	peers   []string
	key     *ecdsa.PrivateKey
	conc    int
	total   int64
	rawAt   func(i int64) []byte
	permits chan struct{} // nil = flat out; else one permit per transaction
	// broadcast sends every frame to every peer (each worker keeps one
	// connection per peer) instead of worker w to peer w % len. Without
	// frame dissemination between nodes (P1b) a rotating leader can only
	// fill blocks from frames it received itself.
	broadcast bool

	frames, txs, busy, failed, conns, rejected atomic.Int64

	// peerBusy counts Busy replies per peer (index into peers); sized by run.
	peerBusy []atomic.Int64
	// Test hooks: nil means sendOneFrame / time.Sleep.
	sendFn func(conn net.Conn, raw, buf []byte) (byte, []byte, error)
	sleep  func(time.Duration)
}

// Busy backoff: the same frame is resent to the same peer after 5 ms,
// doubling to 200 ms, until the peer accepts it or reports a duplicate.
const (
	busyBackoffMin = 5 * time.Millisecond
	busyBackoffMax = 200 * time.Millisecond
)

// busyRetries sums the per-peer Busy retry counters.
func (fr *frameRun) busyRetries() int64 {
	var n int64
	for i := range fr.peerBusy {
		n += fr.peerBusy[i].Load()
	}
	return n
}

// busyByPeer formats the per-peer Busy retry counts, e.g. "a:1=3,b:2=0".
func (fr *frameRun) busyByPeer() string {
	parts := make([]string, len(fr.peers))
	for i, p := range fr.peers {
		var n int64
		if i < len(fr.peerBusy) {
			n = fr.peerBusy[i].Load()
		}
		parts[i] = fmt.Sprintf("%s=%d", p, n)
	}
	return strings.Join(parts, ",")
}

// frameJob is one encoded frame queued for one or more peers; the last peer
// to finish it books the frame as delivered or failed.
type frameJob struct {
	k       int64
	raw     []byte
	ntx     int
	pending atomic.Int32
	failed  atomic.Bool
}

// frameResult summarises a finished -frames run.
type frameResult struct {
	Frames, Txs, Busy, Failed int64
	BusyRetries               int64
	BusyByPeer                string
	Elapsed                   time.Duration
}

func (r frameResult) String() string {
	s := r.Elapsed.Seconds()
	if s <= 0 {
		s = 1e-9
	}
	return fmt.Sprintf("frames=%d txs=%d busy=%d busyRetries=%d failed=%d in %s (%.0f frames/s, %.0f tx/s accepted) busyByPeer=[%s]",
		r.Frames, r.Txs, r.Busy, r.BusyRetries, r.Failed, r.Elapsed.Round(time.Millisecond), float64(r.Frames)/s, float64(r.Txs)/s, r.BusyByPeer)
}

// sendOneFrame writes the magic word and the frame, then reads the reply.
func sendOneFrame(conn net.Conn, raw []byte, buf []byte) (byte, []byte, error) {
	buf = buf[:0]
	var magic [4]byte
	binary.LittleEndian.PutUint32(magic[:], ingest.FrameMagic)
	buf = append(buf, magic[:]...)
	buf = append(buf, raw...)
	if _, err := conn.Write(buf); err != nil {
		return 0, buf, err
	}
	var reply [ingest.FrameReplySize]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return 0, buf, err
	}
	return reply[0], buf, nil
}

func (fr *frameRun) run(progress bool) frameResult {
	nFrames := (fr.total + frame.MaxTxs - 1) / frame.MaxTxs
	fr.peerBusy = make([]atomic.Int64, len(fr.peers))
	var next atomic.Int64
	next.Store(-1)
	start := time.Now()
	stop := make(chan struct{})
	if progress {
		go func() {
			t := time.NewTicker(time.Second)
			defer t.Stop()
			var last int64
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					n := fr.txs.Load()
					fmt.Printf("  frames=%d txs=%d (+%d/s) busy=%d busyRetries=%d failed=%d\n", fr.frames.Load(), n, n-last, fr.busy.Load(), fr.busyRetries(), fr.failed.Load())
					last = n
				}
			}
		}()
	}
	// Per-peer sequential delivery: one goroutine and one connection per
	// peer consume that peer's frames in order, and frame k+1 is sent only
	// after the peer answered accepted or duplicate for frame k (a Busy
	// frame is retried in place). A sliding window over frames would let a
	// later frame enter a full store ahead of a Busy predecessor and leave
	// a nonce gap. fr.conc bounds how many peers send at the same time.
	nPeers := len(fr.peers)
	conc := fr.conc
	if conc < 1 {
		conc = 1
	}
	sem := make(chan struct{}, conc)
	bufN := conc
	if bufN < 4 {
		bufN = 4
	}
	chans := make([]chan *frameJob, nPeers)
	var wg sync.WaitGroup
	for p := range chans {
		chans[p] = make(chan *frameJob, bufN)
		wg.Add(1)
		go func(p int) {
			defer wg.Done()
			var conn net.Conn
			defer func() {
				if conn != nil {
					conn.Close()
				}
			}()
			buf := make([]byte, 0, frame.MaxFrameSize+4)
			for j := range chans[p] {
				sem <- struct{}{}
				var ok bool
				ok, buf = fr.sendTo(&conn, p, j.k, j.raw, j.ntx, buf)
				<-sem
				if !ok {
					j.failed.Store(true)
				}
				if j.pending.Add(-1) == 0 {
					if j.failed.Load() {
						fr.failed.Add(int64(j.ntx))
					} else {
						fr.frames.Add(1)
						fr.txs.Add(int64(j.ntx))
					}
				}
			}
		}(p)
	}
	txs := make([][]byte, 0, frame.MaxTxs)
	var peerSeq = make([]uint64, nPeers)
	for k := int64(0); k < nFrames; k++ {
		lo, hi := k*frame.MaxTxs, (k+1)*frame.MaxTxs
		if hi > fr.total {
			hi = fr.total
		}
		if fr.permits != nil {
			for i := lo; i < hi; i++ {
				<-fr.permits
			}
		}
		txs = txs[:0]
		for i := lo; i < hi; i++ {
			if b := fr.rawAt(i); b != nil {
				txs = append(txs, b)
			}
		}
		// Broadcast: every peer gets every frame with seq k. Otherwise
		// frame k goes to peer k % len with that peer's own contiguous seq.
		if fr.broadcast {
			raw, _, err := frame.Encode(fr.key, uint64(k), frame.TxTypeEthereum, txs)
			if err != nil {
				fr.failed.Add(hi - lo)
				fmt.Printf("  frame %d encode: %v\n", k, err)
				continue
			}
			j := &frameJob{k: k, raw: raw, ntx: len(txs)}
			j.pending.Store(int32(nPeers))
			for p := range chans {
				chans[p] <- j
			}
			continue
		}
		p := int(k % int64(nPeers))
		raw, _, err := frame.Encode(fr.key, peerSeq[p], frame.TxTypeEthereum, txs)
		if err != nil {
			fr.failed.Add(hi - lo)
			fmt.Printf("  frame %d encode: %v\n", k, err)
			continue
		}
		peerSeq[p]++
		j := &frameJob{k: k, raw: raw, ntx: len(txs)}
		j.pending.Store(1)
		chans[p] <- j
	}
	for _, c := range chans {
		close(c)
	}
	wg.Wait()
	close(stop)
	return frameResult{Frames: fr.frames.Load(), Txs: fr.txs.Load(), Busy: fr.busy.Load(), BusyRetries: fr.busyRetries(), BusyByPeer: fr.busyByPeer(), Failed: fr.failed.Load(), Elapsed: time.Since(start)}
}

// sendTo delivers one frame to peer pi over *conn (dialled on demand),
// resending on Busy (unbounded, with backoff) and on a dropped connection.
// It reports whether the peer holds the frame (accepted or duplicate).
func (fr *frameRun) sendTo(conn *net.Conn, pi int, k int64, raw []byte, ntx int, buf []byte) (bool, []byte) {
	peer := fr.peers[pi]
	send := fr.sendFn
	if send == nil {
		send = sendOneFrame
	}
	sleep := fr.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	backoff := busyBackoffMin
	for attempt := 0; ; {
		if *conn == nil {
			c, err := net.DialTimeout("tcp", peer, 2*time.Second)
			if err != nil {
				if attempt++; attempt > 50 {
					return false, buf
				}
				sleep(200 * time.Millisecond)
				continue
			}
			*conn = c
			fr.conns.Add(1)
		}
		status, nb, err := send(*conn, raw, buf)
		buf = nb
		if err != nil {
			(*conn).Close()
			*conn = nil
			if attempt++; attempt > 50 {
				return false, buf
			}
			continue
		}
		switch status {
		case ingest.FrameBusy:
			// Not stored: back off and resend the same frame.
			fr.busy.Add(1)
			fr.peerBusy[pi].Add(1)
			sleep(backoff)
			if backoff *= 2; backoff > busyBackoffMax {
				backoff = busyBackoffMax
			}
			continue
		case ingest.FrameAccepted, ingest.FrameDuplicate:
			return true, buf
		default:
			if n := fr.rejected.Add(1); n <= 5 {
				fmt.Printf("  frame %d (%d txs) rejected by %s: status %d\n", k, ntx, peer, status)
			}
			return false, buf
		}
	}
}
