/*
   Tests for the small standalone helpers across bytes.go, chan.go,
   collections.go, copybytes.go, hextobytes.go, sorted.go and cli.go.
*/

package common

import (
	"context"
	"testing"
	"time"
)

func TestG35ByteCountAndMBToGB(t *testing.T) {
	if ByteCount(500) != "500B" {
		t.Fatalf("expected 500B")
	}
	if ByteCount(1024) != "1.0KB" {
		t.Fatalf("expected 1.0KB")
	}
	v, exp := MBToGB(500)
	if v != 500 || exp != 0 {
		t.Fatalf("expected (500,0), got (%v,%d)", v, exp)
	}
}

func TestG35CopyAndEnsureEnoughSize(t *testing.T) {
	if Copy(nil) != nil {
		t.Fatalf("expected nil copy of nil")
	}
	in := []byte{1, 2, 3}
	out := Copy(in)
	out[0] = 9
	if in[0] == 9 {
		t.Fatalf("expected independent backing array")
	}

	small := make([]byte, 1, 1)
	big := EnsureEnoughSize(small, 10)
	if len(big) != 10 {
		t.Fatalf("expected resized to 10, got %d", len(big))
	}
	withCap := make([]byte, 2, 20)
	reused := EnsureEnoughSize(withCap, 10)
	if len(reused) != 10 {
		t.Fatalf("expected len 10 reusing capacity, got %d", len(reused))
	}
}

func TestG35BitLenToByteLen(t *testing.T) {
	cases := map[int]int{0: 0, 1: 1, 7: 1, 8: 1, 9: 2, 16: 2, 17: 3}
	for bits, want := range cases {
		if got := BitLenToByteLen(bits); got != want {
			t.Fatalf("BitLenToByteLen(%d) = %d, want %d", bits, got, want)
		}
	}
}

func TestG35FastContextErr(t *testing.T) {
	ctx := context.Background()
	if err := FastContextErr(ctx); err != nil {
		t.Fatalf("expected nil for live context, got %v", err)
	}
	ctx2, cancel := context.WithCancel(context.Background())
	cancel()
	if err := FastContextErr(ctx2); err == nil {
		t.Fatalf("expected error for cancelled context")
	}
}

func TestG35Stopped(t *testing.T) {
	if err := Stopped(nil); err != nil {
		t.Fatalf("expected nil for nil channel")
	}
	ch := make(chan struct{})
	if err := Stopped(ch); err != nil {
		t.Fatalf("expected nil for open channel")
	}
	close(ch)
	if err := Stopped(ch); err != ErrStopped {
		t.Fatalf("expected ErrStopped for closed channel, got %v", err)
	}
}

func TestG35SafeClose(t *testing.T) {
	SafeClose(nil) // must not panic

	ch := make(chan struct{})
	SafeClose(ch)
	select {
	case <-ch:
	default:
		t.Fatalf("expected channel to be closed")
	}
	// calling again on an already-closed channel must not panic.
	SafeClose(ch)
}

func TestG35PrioritizedSend(t *testing.T) {
	ch := make(chan int, 2)
	PrioritizedSend(ch, 1)
	PrioritizedSend(ch, 2)
	// channel now full; sending again should drop old messages rather than block.
	done := make(chan struct{})
	go func() {
		PrioritizedSend(ch, 3)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("expected PrioritizedSend to not block on a full channel")
	}
}

func TestG35SliceReverseMapShuffle(t *testing.T) {
	s := []int{1, 2, 3}
	SliceReverse(s)
	if s[0] != 3 || s[2] != 1 {
		t.Fatalf("expected reversed slice, got %v", s)
	}

	mapped := SliceMap([]int{1, 2, 3}, func(x int) string {
		if x == 1 {
			return "a"
		}
		return "x"
	})
	if len(mapped) != 3 || mapped[0] != "a" {
		t.Fatalf("unexpected mapped result: %v", mapped)
	}

	s2 := []int{1, 2, 3, 4, 5}
	SliceShuffle(s2)
	if len(s2) != 5 {
		t.Fatalf("expected shuffle to preserve length")
	}
}

func TestG35CopyBytes(t *testing.T) {
	if CopyBytes(nil) != nil {
		t.Fatalf("expected nil for nil input")
	}
	in := []byte{1, 2, 3}
	out := CopyBytes(in)
	out[0] = 9
	if in[0] == 9 {
		t.Fatalf("expected independent copy")
	}
}

func TestG35Hex2Bytes(t *testing.T) {
	b := Hex2Bytes("0102")
	if len(b) != 2 || b[0] != 1 || b[1] != 2 {
		t.Fatalf("expected [1,2], got %v", b)
	}
	// invalid hex yields an empty result (the error is swallowed).
	if len(Hex2Bytes("zz")) != 0 {
		t.Fatalf("expected empty result for invalid hex")
	}
}

func TestG35SortedKeysAndRemoveDuplicates(t *testing.T) {
	m := map[int]string{3: "c", 1: "a", 2: "b"}
	keys := SortedKeys(m)
	if len(keys) != 3 || keys[0] != 1 || keys[2] != 3 {
		t.Fatalf("expected sorted [1,2,3], got %v", keys)
	}

	deduped := RemoveDuplicatesFromSorted([]int{1, 1, 2, 2, 3})
	if len(deduped) != 3 {
		t.Fatalf("expected 3 unique elements, got %v", deduped)
	}
}

func TestG35CliString2Array(t *testing.T) {
	got := CliString2Array(" a, b ,, c")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestG35RootContext(t *testing.T) {
	ctx, cancel := RootContext()
	select {
	case <-ctx.Done():
		t.Fatalf("expected context not yet done")
	default:
	}
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatalf("expected context to be done after cancel")
	}
}
