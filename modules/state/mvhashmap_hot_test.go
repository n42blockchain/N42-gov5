package state

import (
	"fmt"
	"math/rand"
	"sync"
	"testing"
)

func TestMVHashMapHotHistory(t *testing.T) {
	m := NewMVHashMap(4)
	key := []byte("fee-recipient")
	order := rand.New(rand.NewSource(42)).Perm(300)
	for _, i := range order {
		m.Write(key, Version{TxIdx: i, Incarnation: 1}, []byte(fmt.Sprint(i)))
	}
	for i := 0; i <= 300; i++ {
		value, ver, status := m.Read(key, i)
		if i == 0 {
			if status != MVNotFound {
				t.Fatalf("self visible: %v", status)
			}
			continue
		}
		if status != MVOk || ver.TxIdx != i-1 || string(value) != fmt.Sprint(i-1) {
			t.Fatalf("reader %d: %s %+v %v", i, value, ver, status)
		}
	}
	m.MarkEstimate(key, 150)
	_, ver, status := m.Read(key, 151)
	if status != MVEstimate || ver.TxIdx != 150 {
		t.Fatalf("estimate lost: %+v %v", ver, status)
	}
	m.Write(key, Version{TxIdx: 150, Incarnation: 2}, nil)
	value, ver, status := m.Read(key, 151)
	if status != MVOk || ver.Incarnation != 2 || value != nil {
		t.Fatalf("replacement lost: %+v %v", ver, status)
	}
	m.Delete(key, 150)
	_, ver, status = m.Read(key, 151)
	if status != MVOk || ver.TxIdx != 149 {
		t.Fatalf("deletion lost: %+v %v", ver, status)
	}
	for _, i := range order {
		m.Delete(key, i)
	}
	if _, _, status = m.Read(key, 301); status != MVNotFound {
		t.Fatalf("empty history: %v", status)
	}
}

func BenchmarkMVHotHistory(b *testing.B) {
	for _, count := range []int{32000, 163000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				v := new(versionList)
				for i := 0; i < count; i++ {
					v.upsert(entry{txIdx: i})
				}
				_, idx, _, _, found := v.read(count)
				if !found || idx != count-1 {
					b.Fatal("wrong predecessor")
				}
			}
		})
	}
}

func TestMVHashMapHotConcurrent(t *testing.T) {
	m := NewMVHashMap(4)
	key := []byte("hot-key")
	var wg sync.WaitGroup
	for i := 0; i < 128; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for inc := uint32(0); inc < 4; inc++ {
				m.Write(key, Version{TxIdx: i, Incarnation: inc}, nil)
				m.Read(key, i)
			}
		}(i)
	}
	wg.Wait()
	for i := 1; i <= 128; i++ {
		_, ver, status := m.Read(key, i)
		if status != MVOk || ver.TxIdx != i-1 || ver.Incarnation != 3 {
			t.Fatalf("reader %d: %+v %v", i, ver, status)
		}
	}
}
