package stateless

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// TestAccountLeafEncodeMatchesProduction anchors our self-contained account-leaf
// RLP to the production common/account.StateAccount.EncodeForHashing.
func TestAccountLeafEncodeMatchesProduction(t *testing.T) {
	// Start with a fixed deterministic case for a clear first-failure signal.
	cases := []struct {
		nonce uint64
		bal   uint64
	}{
		{0, 0}, {1, 0}, {0, 1}, {5, 200}, {127, 127}, {128, 128},
		{1000000, 1}, {0xdeadbeef, 0xcafe},
	}
	for ci, c := range cases {
		var sa account.StateAccount
		sa.Reset()
		sa.Nonce = c.nonce
		sa.Balance.SetUint64(c.bal)
		var root, ch types.Hash
		root[0], ch[0] = 0x11, 0x22
		sa.Root, sa.CodeHash = root, ch

		prod := make([]byte, sa.EncodingLengthForHashing())
		sa.EncodeForHashing(prod)

		al := &accountLeaf{nonce: c.nonce, storageRoot: root[:], codeHash: ch[:]}
		al.balance.SetUint64(c.bal)
		ours := al.encode()

		if d := firstDiff(prod, ours); d != -1 {
			t.Fatalf("case %d (nonce=%d bal=%d): first diff at byte %d; prodLen=%d oursLen=%d",
				ci, c.nonce, c.bal, d, len(prod), len(ours))
		}
	}

	// Then fuzz.
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 3000; i++ {
		var sa account.StateAccount
		sa.Reset()
		sa.Nonce = rng.Uint64() >> uint(rng.Intn(64))
		switch rng.Intn(4) {
		case 1:
			sa.Balance.SetUint64(uint64(rng.Intn(128)))
		case 2:
			sa.Balance.SetUint64(rng.Uint64())
		case 3:
			var b [32]byte
			rng.Read(b[:])
			sa.Balance.SetBytes32(b[:])
		}
		var root, ch types.Hash
		rng.Read(root[:])
		rng.Read(ch[:])
		sa.Root, sa.CodeHash = root, ch

		prod := make([]byte, sa.EncodingLengthForHashing())
		sa.EncodeForHashing(prod)

		al := &accountLeaf{nonce: sa.Nonce, storageRoot: root[:], codeHash: ch[:]}
		al.balance.Set(&sa.Balance)
		ours := al.encode()
		if !bytes.Equal(prod, ours) {
			d := firstDiff(prod, ours)
			t.Fatalf("fuzz i=%d: diff at byte %d prodLen=%d oursLen=%d nonce=%d",
				i, d, len(prod), len(ours), sa.Nonce)
		}

		back, err := decodeAccountLeaf(ours)
		if err != nil {
			t.Fatalf("fuzz i=%d decode: %v", i, err)
		}
		if back.nonce != sa.Nonce || back.balance.Cmp(&sa.Balance) != 0 {
			t.Fatalf("fuzz i=%d decode rt mismatch", i)
		}
	}
}
