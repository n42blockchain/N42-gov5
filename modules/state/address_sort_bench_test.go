package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

var addressSortSink []types.Address

// Transaction finalization sorts a few dirty addresses every transaction;
// block finalization sorts the complete dirty set. Keep the previous
// reflection-based implementation as a benchmark reference for both sizes.
func BenchmarkSortedAddresses(b *testing.B) {
	for _, count := range []int{0, 1, 3, 32, 163000} {
		input := make(map[types.Address]struct{}, count)
		for i := 0; i < count; i++ {
			var address types.Address
			binary.BigEndian.PutUint64(address[12:], uint64(i)*0x9e3779b97f4a7c15)
			input[address] = struct{}{}
		}
		for _, name := range []string{"reflect", "generic", "selected"} {
			b.Run(fmt.Sprintf("%d/%s", count, name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if name == "selected" {
						addressSortSink = sortedAddresses(input)
					} else {
						out := make([]types.Address, 0, len(input))
						for address := range input {
							out = append(out, address)
						}
						if name == "generic" {
							slices.SortFunc(out, func(a, b types.Address) int {
								return bytes.Compare(a[:], b[:])
							})
						} else {
							sort.Slice(out, func(i, j int) bool {
								return bytes.Compare(out[i][:], out[j][:]) < 0
							})
						}
						addressSortSink = out
					}
				}
			})
		}
	}
}
