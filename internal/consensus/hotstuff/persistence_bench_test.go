// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Write-amplification measurement for the durable vote journal.
//
// Every vote now costs one MDBX write transaction on the consensus critical
// path, so the per-write latency has to stay far below the view budget. Run:
//
//	go test -tags "nosqlite,noboltdb" -run XXX -bench BenchmarkVoteJournalWrite \
//	    ./internal/consensus/hotstuff/
//
// Skipped unless -bench is given; it creates a real on-disk MDBX environment
// with the production sync mode (Durable — fsync per commit), because an
// in-memory test DB opens with UtterlyNoSync and would report a meaningless
// number.

package hotstuff

import (
	"bytes"
	"context"
	"sort"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// benchState builds a ConsensusState the size a live 7-validator chain writes.
func benchState(view ViewNumber, signers int) *ConsensusState {
	bits := make([]bool, signers)
	for i := range bits {
		bits[i] = true
	}
	qc := QuorumCertificate{
		View:               view - 1,
		BlockHash:          types.Hash{0xAA, 0xBB, 0xCC},
		AggregateSignature: bytes.Repeat([]byte{0x5A}, 96),
		Signers:            bits,
	}
	return &ConsensusState{
		View:                view,
		ConsecutiveTimeouts: 0,
		LockedQC:            qc,
		LastCommittedQC:     qc,
		LastVotedView:       view,
		LastVotedHash:       types.Hash{0x11},
		LastCommitVotedView: view - 1,
		LastCommitVotedHash: types.Hash{0x22},
	}
}

// BenchmarkVoteJournalWriteContended measures the journal write while another
// writer keeps the MDBX writer lock busy with block-sized transactions. This is
// the case that matters: the vote journal runs on the consensus critical path
// with the engine mutex held, and chaindata has a single writer shared with
// block import.
func BenchmarkVoteJournalWriteContended(b *testing.B) {
	// gap is the idle time the competing writer leaves between block-sized
	// transactions. 0 = a saturated writer (catch-up / bulk import); 200ms is
	// roughly a busy live chain producing 5 blocks a second.
	for _, tc := range []struct {
		name string
		gap  time.Duration
	}{
		{"saturated", 0},
		{"block_paced_200ms", 200 * time.Millisecond},
	} {
		b.Run(tc.name, func(b *testing.B) { benchJournalContended(b, tc.gap) })
	}
}

func benchJournalContended(b *testing.B, gap time.Duration) {
	dir := b.TempDir()
	db, err := mdbx.NewMDBX(log.New()).Path(dir).Open(context.Background())
	if err != nil {
		b.Fatalf("open mdbx: %v", err)
	}
	defer db.Close()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		payload := bytes.Repeat([]byte{0x7E}, 4096)
		key := make([]byte, 8)
		var n uint64
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = db.Update(context.Background(), func(tx kv.RwTx) error {
				for i := 0; i < 256; i++ { // ~1 MB per tx, block-commit shaped
					n++
					for j := 0; j < 8; j++ {
						key[j] = byte(n >> (8 * j))
					}
					if err := tx.Put(modules.HotStuffState, append([]byte("bench/"), key...), payload); err != nil {
						return err
					}
				}
				return nil
			})
			if gap > 0 {
				select {
				case <-stop:
					return
				case <-time.After(gap):
				}
			}
		}
	}()

	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := benchState(ViewNumber(1000+i), 7)
		t0 := time.Now()
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			return SaveConsensusState(tx, s)
		}); err != nil {
			b.Fatalf("journal write: %v", err)
		}
		samples = append(samples, time.Since(t0))
	}
	b.StopTimer()
	close(stop)
	<-done

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	us := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1000 }
	b.ReportMetric(us(samples[len(samples)/2]), "p50_us")
	b.ReportMetric(us(samples[len(samples)*90/100]), "p90_us")
	b.ReportMetric(us(samples[len(samples)*99/100]), "p99_us")
	b.ReportMetric(us(samples[len(samples)-1]), "max_us")
}

func BenchmarkVoteJournalWrite(b *testing.B) {
	dir := b.TempDir()
	db, err := mdbx.NewMDBX(log.New()).Path(dir).Open(context.Background())
	if err != nil {
		b.Fatalf("open mdbx: %v", err)
	}
	defer db.Close()

	// Report the on-disk record size once, so growth over the legacy layout is
	// visible alongside the latency.
	st := benchState(1000, 7)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, st)
	}); err != nil {
		b.Fatalf("seed write: %v", err)
	}
	var recordSize int
	_ = db.View(context.Background(), func(tx kv.Tx) error {
		v, _ := tx.GetOne(modules.HotStuffState, hotstuffStateKey)
		recordSize = len(v)
		return nil
	})

	samples := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := benchState(ViewNumber(1000+i), 7)
		t0 := time.Now()
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			return SaveConsensusState(tx, s)
		}); err != nil {
			b.Fatalf("journal write: %v", err)
		}
		samples = append(samples, time.Since(t0))
	}
	b.StopTimer()

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	if len(samples) > 0 {
		us := func(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1000 }
		b.ReportMetric(us(samples[len(samples)/2]), "p50_us")
		b.ReportMetric(us(samples[len(samples)*90/100]), "p90_us")
		b.ReportMetric(us(samples[len(samples)*99/100]), "p99_us")
		b.ReportMetric(us(samples[len(samples)-1]), "max_us")
		b.ReportMetric(float64(recordSize), "record_bytes")
	}
}
