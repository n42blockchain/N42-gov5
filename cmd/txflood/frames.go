package main

import (
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/ingest"
)

// frameRun is the -frames mode: the run's pre-signed transactions are packed
// frame.MaxTxs at a time into gateway-signed frames and sent over the binary
// ingest port (internal/ingest) instead of JSON-RPC. Each worker owns one
// connection and is paced by the acks: one frame in flight per connection.
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
}

// frameResult summarises a finished -frames run.
type frameResult struct {
	Frames, Txs, Busy, Failed int64
	Elapsed                   time.Duration
}

func (r frameResult) String() string {
	s := r.Elapsed.Seconds()
	if s <= 0 {
		s = 1e-9
	}
	return fmt.Sprintf("frames=%d txs=%d busy=%d failed=%d in %s (%.0f frames/s, %.0f tx/s accepted)",
		r.Frames, r.Txs, r.Busy, r.Failed, r.Elapsed.Round(time.Millisecond), float64(r.Frames)/s, float64(r.Txs)/s)
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
					fmt.Printf("  frames=%d txs=%d (+%d/s) busy=%d failed=%d\n", fr.frames.Load(), n, n-last, fr.busy.Load(), fr.failed.Load())
					last = n
				}
			}
		}()
	}
	var wg sync.WaitGroup
	for w := 0; w < fr.conc; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			targets := []int{w % len(fr.peers)}
			if fr.broadcast {
				targets = make([]int, len(fr.peers))
				for i := range targets {
					targets[i] = i
				}
			}
			conns := make([]net.Conn, len(fr.peers))
			defer func() {
				for _, c := range conns {
					if c != nil {
						c.Close()
					}
				}
			}()
			buf := make([]byte, 0, frame.MaxFrameSize+4)
			txs := make([][]byte, 0, frame.MaxTxs)
			for {
				k := next.Add(1)
				if k >= nFrames {
					return
				}
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
				raw, _, err := frame.Encode(fr.key, uint64(k), frame.TxTypeEthereum, txs)
				if err != nil {
					fr.failed.Add(hi - lo)
					fmt.Printf("  frame %d encode: %v\n", k, err)
					continue
				}
				delivered := false
				for _, p := range targets {
					var ok bool
					ok, buf = fr.sendTo(&conns[p], fr.peers[p], k, raw, len(txs), buf)
					delivered = delivered || ok
				}
				if delivered {
					fr.frames.Add(1)
					fr.txs.Add(int64(len(txs)))
				} else {
					fr.failed.Add(int64(len(txs)))
				}
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	return frameResult{Frames: fr.frames.Load(), Txs: fr.txs.Load(), Busy: fr.busy.Load(), Failed: fr.failed.Load(), Elapsed: time.Since(start)}
}

// sendTo delivers one frame to one peer over *conn (dialled on demand),
// resending on busy and on a dropped connection. It reports whether the
// peer holds the frame (accepted or duplicate).
func (fr *frameRun) sendTo(conn *net.Conn, peer string, k int64, raw []byte, ntx int, buf []byte) (bool, []byte) {
	for attempt := 0; ; attempt++ {
		if *conn == nil {
			c, err := net.DialTimeout("tcp", peer, 2*time.Second)
			if err != nil {
				if attempt > 50 {
					return false, buf
				}
				time.Sleep(200 * time.Millisecond)
				continue
			}
			*conn = c
			fr.conns.Add(1)
		}
		status, nb, err := sendOneFrame(*conn, raw, buf)
		buf = nb
		if err != nil {
			(*conn).Close()
			*conn = nil
			if attempt > 50 {
				return false, buf
			}
			continue
		}
		switch status {
		case ingest.FrameBusy:
			// Not stored: back off and resend the same frame.
			fr.busy.Add(1)
			d := time.Duration(attempt+1) * 2 * time.Millisecond
			if d > 100*time.Millisecond {
				d = 100 * time.Millisecond
			}
			time.Sleep(d)
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
