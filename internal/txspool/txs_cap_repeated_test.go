package txspool

import (
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
)

func TestRepeatedCapAfterHeapMutations(t *testing.T) {
	for name, mutate := range map[string]func(*txsSortedMap){
		"none":         func(*txsSortedMap) {},
		"insert low":   func(m *txsSortedMap) { m.Put(newTestTx(1, 100)) },
		"replace":      func(m *txsSortedMap) { m.Put(newTestTx(20, 200)) },
		"forward":      func(m *txsSortedMap) { m.Forward(30) },
		"ready":        func(m *txsSortedMap) { m.Ready(10) },
		"remove large": func(m *txsSortedMap) { m.Remove(30) },
		"remove small": func(m *txsSortedMap) { m.Cap(200); m.Remove(30) },
		"filter": func(m *txsSortedMap) {
			m.Filter(func(tx *transaction.Transaction) bool { return tx.Nonce()%6 == 0 })
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := newTxSortedMap()
			// Gaps make Ready remove only a prefix; a large initial heap also
			// exercises Remove's rebuild path. First Cap establishes ordering.
			for i := 0; i < 512; i++ {
				m.Put(newTestTx(uint64(10+2*i), 100))
			}
			m.Cap(500)
			m.Flatten() // check cache truncation as well as the heap
			mutate(m)
			want := make([]uint64, 0, len(m.items))
			for nonce := range m.items {
				want = append(want, nonce)
			}
			sort.Slice(want, func(i, j int) bool { return want[i] < want[j] })
			for len(want) > 16 {
				drops := m.Cap(len(want) - 1)
				if len(drops) != 1 || drops[0].Nonce() != want[len(want)-1] {
					t.Fatalf("Cap did not evict highest nonce %d", want[len(want)-1])
				}
				want = want[:len(want)-1]
			}
			flat := m.Flatten()
			if len(flat) != len(want) || m.index.Len() != len(want) {
				t.Fatal("map, heap and cache lengths differ")
			}
			for i := range want {
				if flat[i].Nonce() != want[i] {
					t.Fatal("retained nonce set changed")
				}
				if i > 0 && (*m.index)[(i-1)/2] > (*m.index)[i] {
					t.Fatal("remaining index is not a min-heap")
				}
			}
		})
	}
}

func BenchmarkTxSortedMapRepeatedCap(b *testing.B) {
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		m := newTxSortedMap()
		for nonce := uint64(0); nonce < 6000; nonce++ {
			m.Put(newTestTx(nonce, 100))
		}
		b.StartTimer()
		for size := 5999; size >= 32; size-- {
			m.Cap(size)
		}
	}
}
