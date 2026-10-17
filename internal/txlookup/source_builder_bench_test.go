package txlookup

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// BenchmarkBuildSegmentFile builds one sealer-sized segment: 7 blocks of
// 163,000 transactions (1,141,000 keys), the S101 fleet's segment.
func BenchmarkBuildSegmentFile(b *testing.B) {
	const blocks, perBlockTxs = 7, 163_000
	const start = 1_000_000
	perBlock := make([][]types.Hash, blocks)
	var buf [16]byte
	for bl := range perBlock {
		perBlock[bl] = make([]types.Hash, perBlockTxs)
		for i := range perBlock[bl] {
			binary.BigEndian.PutUint64(buf[:], uint64(bl))
			binary.BigEndian.PutUint64(buf[8:], uint64(i))
			perBlock[bl][i] = crypto.Keccak256Hash(buf[:])
		}
	}
	src := memSource(start, perBlock)
	dir := b.TempDir()
	b.ResetTimer()
	var size int64
	for it := 0; it < b.N; it++ {
		seg, err := BuildSegmentFile(context.Background(), dir, start, start+blocks, src)
		if err != nil {
			b.Fatal(err)
		}
		if st, err := os.Stat(seg.idxPath); err == nil {
			size = st.Size()
		}
		seg.Discard()
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*blocks*perBlockTxs), "ns/key")
	b.ReportMetric(float64(size*8)/float64(blocks*perBlockTxs), "bits/key")
}

func BenchmarkBuildSegmentFileLeaf(b *testing.B) {
	for _, leaf := range []uint16{8, 6, 5, 4} {
		b.Run(fmt.Sprintf("leaf%d", leaf), func(b *testing.B) {
			prev := sourceLeafSize
			sourceLeafSize = leaf
			defer func() { sourceLeafSize = prev }()
			BenchmarkBuildSegmentFile(b)
		})
	}
}

func BenchmarkBuildSegmentFileBucket(b *testing.B) {
	for _, bucket := range []int{2000, 1000, 500, 200, 100} {
		b.Run(fmt.Sprintf("bucket%d", bucket), func(b *testing.B) {
			prev := sourceBucketSize
			sourceBucketSize = bucket
			defer func() { sourceBucketSize = prev }()
			BenchmarkBuildSegmentFile(b)
		})
	}
}

// BenchmarkSegmentLookup resolves keys of one 163k-key segment built with
// the 2000/5 parameters and with the current ones.
func BenchmarkSegmentLookup(b *testing.B) {
	for _, p := range []struct {
		bucket int
		leaf   uint16
	}{{2000, 5}, {sourceBucketSize, sourceLeafSize}} {
		b.Run(fmt.Sprintf("bucket%d_leaf%d", p.bucket, p.leaf), func(b *testing.B) {
			pb, pl := sourceBucketSize, sourceLeafSize
			sourceBucketSize, sourceLeafSize = p.bucket, p.leaf
			defer func() { sourceBucketSize, sourceLeafSize = pb, pl }()
			const start, perBlockTxs = 1_000, 163_000
			hashes := make([]types.Hash, perBlockTxs)
			var buf [8]byte
			for i := range hashes {
				binary.BigEndian.PutUint64(buf[:], uint64(i))
				hashes[i] = crypto.Keccak256Hash(buf[:])
			}
			dir := b.TempDir()
			if err := BuildSegmentFromSource(context.Background(), dir, start, start+1,
				memSource(start, [][]types.Hash{hashes})); err != nil {
				b.Fatal(err)
			}
			svc, err := NewService(dir)
			if err != nil {
				b.Fatal(err)
			}
			defer svc.Close()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if got, err := svc.Lookup(nil, hashes[i%perBlockTxs]); err != nil || got == nil {
					b.Fatal("lookup failed", err)
				}
			}
		})
	}
}
