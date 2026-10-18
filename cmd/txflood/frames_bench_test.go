package main

import (
	"encoding/binary"
	"io"
	"net"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/ingest"
)

// stubIngest is a minimal FRM1 sink: it reads each frame into one reused
// buffer and answers Accepted, so a profile of the benchmark below is the
// generator's own cost plus the kernel's loopback copy on the receive side
// (functions under stubIngest).
func stubIngest(tb testing.TB) (addr string, stop func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go stubIngestConn(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func stubIngestConn(c net.Conn) {
	defer c.Close()
	buf := make([]byte, frame.MaxFrameSize)
	var reply [ingest.FrameReplySize]byte
	for {
		if _, err := io.ReadFull(c, buf[:4+frame.HeaderSize]); err != nil {
			return
		}
		h, err := frame.ParseHeader(buf[4:])
		if err != nil {
			return
		}
		if _, err := io.ReadFull(c, buf[:int(h.PayloadLen)+frame.SigSize]); err != nil {
			return
		}
		reply[0] = ingest.FrameAccepted
		if _, err := c.Write(reply[:]); err != nil {
			return
		}
	}
}

// BenchmarkFrameReplay is one generator in the rig's shape: a presigned
// corpus of 500-transfer frames (110-byte transactions) replayed flat out
// with -frame-broadcast to 7 peers at -conc 8 (the rig's r35zzzcs flags). Reported as process CPU
// (user + system, the stub peers included) per transaction delivered.
//
// Run: GOMAXPROCS=4 go test -tags nosqlite,noboltdb -run XXX -bench BenchmarkFrameReplay ./cmd/txflood/
func BenchmarkFrameReplay(b *testing.B) {
	const (
		peers  = 7
		frames = 400
		txLen  = 110
	)
	var addrs []string
	for i := 0; i < peers; i++ {
		a, stop := stubIngest(b)
		defer stop()
		addrs = append(addrs, a)
	}
	gw, _ := crypto.GenerateKey()
	total := int64(frames * frame.MaxTxs)
	tx := make([]byte, txLen)
	for i := range tx {
		tx[i] = byte(i*7 + 1)
	}
	path := filepath.Join(b.TempDir(), "corpus.bin")
	pre := &frameRun{peers: addrs, key: gw, total: total, broadcast: true, workers: 4,
		rawAt: func(i int64) []byte {
			out := append([]byte(nil), tx...)
			binary.LittleEndian.PutUint64(out[txLen-8:], uint64(i))
			return out
		}}
	if err := pre.writeCorpus(path); err != nil {
		b.Fatal(err)
	}
	cpu := func() time.Duration {
		var ru syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
		return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	}
	b.ResetTimer()
	start := cpu()
	for i := 0; i < b.N; i++ {
		c, err := openCorpus(path)
		if err != nil {
			b.Fatal(err)
		}
		fr := &frameRun{peers: addrs, key: gw, conc: 8, total: total, broadcast: true, corpus: c}
		if res := fr.run(false); res.Txs != total || res.Failed != 0 {
			b.Fatalf("result %s", res)
		}
	}
	b.ReportMetric(float64((cpu()-start).Nanoseconds())/float64(int64(b.N)*total), "cpu-ns/tx")
	b.ReportMetric(float64(int64(b.N)*total)/b.Elapsed().Seconds(), "tx/s")
}
