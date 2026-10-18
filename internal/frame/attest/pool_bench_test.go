// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package attest

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// poolBench is MaxEntries frames whose committee (7 validators, f = 2,
// size 3) is validators 0, 1 and 2, each member's batch of them encoded
// with the full senders lists, and a signature check that always passes,
// so the benchmark measures the pool's own work (decode, digest, record)
// and not BLS.
type poolBench struct {
	v      *Validators
	hashes []types.Hash
	lists  [][]types.Address
	bodies [][]byte // bodies[k]: member k's batch
}

func newPoolBench(b *testing.B) *poolBench {
	t := &testing.T{}
	addrs := SortAddresses(randAddrs(t, 7))
	pb := &poolBench{v: &Validators{Sorted: addrs, F: 2, Verify: func(types.Address, []byte, []byte) bool { return true }}}
	for len(pb.hashes) < MaxEntries {
		h := randHash(t)
		if c := Committee(h, 7, 3); c[0] != 0 {
			continue
		}
		pb.hashes = append(pb.hashes, h)
		pb.lists = append(pb.lists, randAddrs(t, frame.MaxTxs))
	}
	for k := 0; k < 3; k++ {
		es := make([]Entry, len(pb.hashes))
		for i, h := range pb.hashes {
			es[i] = Entry{Frame: h, Digest: Digest(pb.lists[i]), Senders: pb.lists[i]}
		}
		pb.bodies = append(pb.bodies, EncodeBody(&Batch{Attester: addrs[k], Entries: es, Sig: make([]byte, 96)}))
	}
	return pb
}

func (pb *poolBench) pool(self types.Address) *Pool {
	signer := transaction.LatestSignerForChainID(big.NewInt(1337))
	return NewPool(Config{Validators: func() *Validators { return pb.v }, Self: func() types.Address { return self },
		Signer: func() transaction.Signer { return signer }, Store: store.New(1 << 20), CommitteeSize: 3,
		Sign: func([]byte) ([]byte, error) { return make([]byte, 96), nil }, Send: func([]byte) {}})
}

// BenchmarkPoolReceive: a node off the committee takes the three members'
// batches of MaxEntries frames (500 senders each) and accepts every frame.
// Reported per frame.
func BenchmarkPoolReceive(b *testing.B) {
	pb := newPoolBench(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := pb.pool(pb.v.Sorted[6])
		for _, body := range pb.bodies {
			p.HandleWire(body)
		}
		if p.Stats().Accepted != uint64(len(pb.hashes)) {
			b.Fatalf("accepted %d", p.Stats().Accepted)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(pb.hashes)), "ns/frame")
}

// BenchmarkPoolMember: a committee member recovers every frame itself
// (Recovered queues its own attestation) and then takes the other two
// members' batches. Reported per frame.
func BenchmarkPoolMember(b *testing.B) {
	pb := newPoolBench(b)
	ss := make([]*store.Senders, len(pb.hashes))
	for i := range ss {
		ss[i] = &store.Senders{Addrs: pb.lists[i]}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p := pb.pool(pb.v.Sorted[0])
		for k, h := range pb.hashes {
			p.Recovered(h, ss[k])
		}
		for _, body := range pb.bodies[1:] {
			p.HandleWire(body)
		}
		if p.Stats().BadDigest != 0 || p.Stats().Entries != uint64(2*len(pb.hashes)) {
			b.Fatalf("stats %s", p.Stats())
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(pb.hashes)), "ns/frame")
}
