package main

import "testing"

func TestSenderNonceAtAndClaimed(t *testing.T) {
	const S, P = 4, 3
	for _, il := range []bool{false, true} {
		seen := map[[2]int]bool{}
		for i := int64(0); i < S*P; i++ {
			s, j := senderNonceAt(i, S, P, il)
			if s < 0 || s >= S || j < 0 || j >= P || seen[[2]int{s, j}] {
				t.Fatalf("il=%v i=%d bad (%d,%d)", il, i, s, j)
			}
			seen[[2]int{s, j}] = true
			if senderIndexAt(i, S, P, il) != s {
				t.Fatalf("senderIndexAt mismatch")
			}
		}
		// claimed accounting must equal a direct count over the first c indexes.
		for c := int64(0); c <= S*P+2; c++ {
			for s := 0; s < S; s++ {
				var want int64
				for i := int64(0); i < c && i < S*P; i++ {
					if senderIndexAt(i, S, P, il) == s {
						want++
					}
				}
				if got := claimedOfSender(c, s, S, P, il); got != want {
					t.Fatalf("il=%v c=%d s=%d got %d want %d", il, c, s, got, want)
				}
			}
		}
	}
	if s, j := senderNonceAt(5, S, P, false); s != 1 || j != 2 {
		t.Fatalf("major: %d %d", s, j)
	}
	if s, j := senderNonceAt(5, S, P, true); s != 1 || j != 1 {
		t.Fatalf("interleave: %d %d", s, j)
	}
	// routing: shard by sender across 3 urls
	for i := int64(0); i < 12; i++ {
		s, _ := senderNonceAt(i, S, P, true)
		if senderIndexAt(i, S, P, true)%3 != s%3 {
			t.Fatal("route")
		}
	}
}
