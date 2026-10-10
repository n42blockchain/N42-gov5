package state

import (
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// reuseMap keeps and empties a small map, and replaces one that held more
// than smallMapReuse entries (its bucket array would stay at the high-water
// size).
func TestReuseMap(t *testing.T) {
	small := map[types.Address]int{{1}: 1, {2}: 2}
	got := reuseMap(small)
	if len(got) != 0 || reflect.ValueOf(got).UnsafePointer() != reflect.ValueOf(small).UnsafePointer() {
		t.Fatalf("small map: len %d, same map %v; want it emptied and kept", len(got), reflect.ValueOf(got).UnsafePointer() == reflect.ValueOf(small).UnsafePointer())
	}
	big := make(map[types.Address]int)
	for i := 0; i <= smallMapReuse; i++ {
		big[types.Address{byte(i), 1}] = i
	}
	got = reuseMap(big)
	if len(got) != 0 || reflect.ValueOf(got).UnsafePointer() == reflect.ValueOf(big).UnsafePointer() {
		t.Fatal("large map: want a fresh empty map")
	}
	if len(big) != smallMapReuse+1 {
		t.Fatal("large map: the old map must be left alone")
	}
	if m := reuseMap[types.Address, int](nil); m == nil {
		t.Fatal("nil map: want a usable map")
	}
}
