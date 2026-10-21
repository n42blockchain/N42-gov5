package main

import (
	"bufio"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
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
	// workers is the number of goroutines that sign and encode frames ahead
	// of the in-order dispatcher (<= 1: one). Frame k's content and seq are
	// functions of k alone, so frames can be built out of order and consumed
	// in order.
	workers int
	// corpus, when set, replaces building: pre-encoded frames are streamed
	// from it in order (see -presign / -replay).
	corpus *corpusReader

	presigned, streamed                                  atomic.Int64
	frames, txs, busy, failed, conns, rejected, timeouts atomic.Int64

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

// frameReplyTimeout bounds one frame write plus its reply.
const frameReplyTimeout = 10 * time.Second

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
// The two go out in one writev (net.Buffers), so the frame is not copied
// into a send buffer first: with -frame-broadcast every frame went through
// that copy once per peer. buf is returned unused (kept for the hook's
// signature).
func sendOneFrame(conn net.Conn, raw []byte, buf []byte) (byte, []byte, error) {
	var magic [4]byte
	binary.LittleEndian.PutUint32(magic[:], ingest.FrameMagic)
	// One deadline covers the write and the reply wait, so a peer that stops
	// answering cannot park the sender forever.
	_ = conn.SetDeadline(time.Now().Add(frameReplyTimeout))
	bufs := net.Buffers{magic[:], raw}
	if _, err := bufs.WriteTo(conn); err != nil {
		return 0, buf, err
	}
	var reply [ingest.FrameReplySize]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return 0, buf, err
	}
	_ = conn.SetDeadline(time.Time{})
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
					fmt.Printf("  frames=%d txs=%d (+%d/s) busy=%d busyRetries=%d failed=%d timeouts=%d presigned=%d streamed=%d\n", fr.frames.Load(), n, n-last, fr.busy.Load(), fr.busyRetries(), fr.failed.Load(), fr.timeouts.Load(), fr.presigned.Load(), fr.streamed.Load())
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
			var buf []byte
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
					if fr.corpus != nil {
						// Every peer is done with it: the corpus reader
						// reuses the buffer for a later frame.
						fr.corpus.recycle(j.raw)
					}
				}
			}
		}(p)
	}
	var src <-chan builtFrame
	if fr.corpus != nil {
		src = fr.corpus.stream(nFrames, &fr.streamed)
	} else {
		src = fr.produce(nFrames)
	}
	for bf := range src {
		k := bf.k
		lo, hi := k*frame.MaxTxs, (k+1)*frame.MaxTxs
		if hi > fr.total {
			hi = fr.total
		}
		if fr.permits != nil {
			for i := lo; i < hi; i++ {
				<-fr.permits
			}
		}
		if bf.err != nil {
			fr.failed.Add(hi - lo)
			fmt.Printf("  frame %d encode: %v\n", k, bf.err)
			continue
		}
		// Broadcast: every peer gets every frame with seq k. Otherwise
		// frame k goes to peer k % len with that peer's own contiguous seq.
		j := &frameJob{k: k, raw: bf.raw, ntx: bf.ntx}
		if fr.broadcast {
			j.pending.Store(int32(nPeers))
			for p := range chans {
				chans[p] <- j
			}
			continue
		}
		j.pending.Store(1)
		chans[int(k%int64(nPeers))] <- j
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
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				fr.timeouts.Add(1)
			}
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

// builtFrame is one encoded frame (or the error that stopped its encoding).
type builtFrame struct {
	k   int64
	raw []byte
	ntx int
	err error
}

// buildFrame signs-and-encodes frame k. The seq is k when broadcasting, else
// k / peers (peer k % peers receives every peers-th frame, contiguously).
func (fr *frameRun) buildFrame(k int64) builtFrame {
	lo, hi := k*frame.MaxTxs, (k+1)*frame.MaxTxs
	if hi > fr.total {
		hi = fr.total
	}
	txs := make([][]byte, 0, hi-lo)
	for i := lo; i < hi; i++ {
		if b := fr.rawAt(i); b != nil {
			txs = append(txs, b)
		}
	}
	seq := uint64(k)
	if !fr.broadcast {
		seq = uint64(k) / uint64(len(fr.peers))
	}
	raw, _, err := frame.Encode(fr.key, seq, frame.TxTypeEthereum, txs)
	return builtFrame{k: k, raw: raw, ntx: len(txs), err: err}
}

// produce builds frames 0..nFrames-1 on fr.workers goroutines and delivers
// them in index order. Lookahead is bounded to a few frames per worker.
func (fr *frameRun) produce(nFrames int64) <-chan builtFrame {
	w := fr.workers
	if w < 1 {
		w = 1
	}
	out := make(chan builtFrame)
	type job struct {
		k int64
		f chan builtFrame
	}
	work := make(chan job, w)
	futures := make(chan chan builtFrame, 2*w)
	for i := 0; i < w; i++ {
		go func() {
			for j := range work {
				j.f <- fr.buildFrame(j.k)
			}
		}()
	}
	go func() {
		for k := int64(0); k < nFrames; k++ {
			f := make(chan builtFrame, 1)
			futures <- f
			work <- job{k, f}
		}
		close(work)
		close(futures)
	}()
	go func() {
		for f := range futures {
			out <- <-f
		}
		close(out)
	}()
	return out
}

// Corpus file: header, then per frame u32 rawLen, u32 txCount, raw bytes.
// The frames are signed, so the magic records the frame hash version they
// were signed under: a corpus replays only with the same N42_FRAME_HASH_V2
// setting the fleet runs.
const (
	corpusMagic   = "TXFC0001" // frame hash v1
	corpusMagicV2 = "TXFC2H01" // frame hash v2
)

func corpusMagicFor(v2 bool) string {
	if v2 {
		return corpusMagicV2
	}
	return corpusMagic
}

type corpusHeader struct {
	Peers     uint32
	Broadcast uint8
	Total     uint64
	Frames    uint64
}

// writeCorpus builds every frame (fr.workers goroutines) into path.
func (fr *frameRun) writeCorpus(path string) error {
	nFrames := (fr.total + frame.MaxTxs - 1) / frame.MaxTxs
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	h := corpusHeader{Peers: uint32(len(fr.peers)), Total: uint64(fr.total), Frames: uint64(nFrames)}
	if fr.broadcast {
		h.Broadcast = 1
	}
	_, _ = w.WriteString(corpusMagicFor(frame.HashV2Enabled()))
	_ = binary.Write(w, binary.LittleEndian, h)
	for bf := range fr.produce(nFrames) {
		if bf.err != nil {
			f.Close()
			return fmt.Errorf("frame %d: %w", bf.k, bf.err)
		}
		var hd [8]byte
		binary.LittleEndian.PutUint32(hd[0:], uint32(len(bf.raw)))
		binary.LittleEndian.PutUint32(hd[4:], uint32(bf.ntx))
		_, _ = w.Write(hd[:])
		_, _ = w.Write(bf.raw)
		fr.presigned.Add(int64(bf.ntx))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// corpusReader streams a corpus file's frames in order.
type corpusReader struct {
	f *os.File
	r *bufio.Reader
	h corpusHeader
	// hashV2: the frames were signed under frame hash v2.
	hashV2 bool
	// free holds frame buffers every peer has finished with. Streaming
	// allocated (and zeroed) a fresh ~56 KB buffer per frame only to
	// overwrite it, which with the GC it drove was a fifth of a replaying
	// generator's CPU.
	free sync.Pool
}

// recycle hands back a frame buffer stream returned once nothing uses it.
func (c *corpusReader) recycle(raw []byte) {
	if cap(raw) > 0 {
		c.free.Put(&raw)
	}
}

// frameBuf returns a buffer of n bytes, reused when one is free. Its
// contents are overwritten by the caller.
func (c *corpusReader) frameBuf(n int) []byte {
	if p, ok := c.free.Get().(*[]byte); ok && cap(*p) >= n {
		return (*p)[:n]
	}
	return make([]byte, n)
}

func openCorpus(path string) (*corpusReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(f, 4<<20)
	var m [len(corpusMagic)]byte
	if _, err := io.ReadFull(r, m[:]); err != nil || (string(m[:]) != corpusMagic && string(m[:]) != corpusMagicV2) {
		f.Close()
		return nil, fmt.Errorf("%s: not a txflood corpus", path)
	}
	c := &corpusReader{f: f, r: r, hashV2: string(m[:]) == corpusMagicV2}
	if err := binary.Read(r, binary.LittleEndian, &c.h); err != nil {
		f.Close()
		return nil, err
	}
	return c, nil
}

// check verifies the corpus was built for this run's shape.
func (c *corpusReader) check(peers int, broadcast bool, total int64) error {
	if int(c.h.Peers) != peers || (c.h.Broadcast == 1) != broadcast || int64(c.h.Total) != total {
		return fmt.Errorf("corpus built for peers=%d broadcast=%v total=%d, run has peers=%d broadcast=%v total=%d",
			c.h.Peers, c.h.Broadcast == 1, c.h.Total, peers, broadcast, total)
	}
	if v2 := frame.HashV2Enabled(); c.hashV2 != v2 {
		return fmt.Errorf("corpus frames signed with frame hash v2=%v, run has %s=%v", c.hashV2, frame.EnvFrameHashV2, v2)
	}
	return nil
}

func (c *corpusReader) stream(nFrames int64, streamed *atomic.Int64) <-chan builtFrame {
	out := make(chan builtFrame, 8)
	go func() {
		defer close(out)
		defer c.f.Close()
		for k := int64(0); k < nFrames; k++ {
			var hd [8]byte
			if _, err := io.ReadFull(c.r, hd[:]); err != nil {
				out <- builtFrame{k: k, err: err}
				continue
			}
			n := binary.LittleEndian.Uint32(hd[0:])
			if n > frame.MaxFrameSize {
				out <- builtFrame{k: k, err: fmt.Errorf("corpus frame %d of %d bytes", k, n)}
				continue
			}
			raw := c.frameBuf(int(n))
			if _, err := io.ReadFull(c.r, raw); err != nil {
				out <- builtFrame{k: k, err: err}
				continue
			}
			ntx := int(binary.LittleEndian.Uint32(hd[4:]))
			streamed.Add(int64(ntx))
			out <- builtFrame{k: k, raw: raw, ntx: ntx}
		}
	}()
	return out
}
