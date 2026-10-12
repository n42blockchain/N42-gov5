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
