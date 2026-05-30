package stateless

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// buildMyAccountRoot builds the standard-Ethereum account-trie root using THIS
// package's MPT (fullTrie + accountLeaf.encode + per-account storage subtree),
// from the same plaintext (accts, stor) the production TrieRootComputer takes.
// We keccak addresses/slots to hashed keys exactly as production does.
func buildMyAccountRoot(accts map[types.Address]*account.StateAccount, stor map[types.Address]map[types.Hash]*uint256.Int) []byte {
	at := fullTrie()
	for addr, a := range accts {
		ah := keccak(addr[:])
		st := fullTrie()
		any := false
		if slots, ok := stor[addr]; ok {
			for slot, v := range slots {
				if v == nil || v.IsZero() {
					continue
				}
				st.update(keybytesToHex(keccak(slot[:])), rlpStr(v.Bytes()))
				any = true
			}
		}
		sr := append([]byte(nil), emptyRootHash...)
		if any {
			sr = st.hash()
		}
		ch := a.CodeHash
		if ch == (types.Hash{}) {
			ch = types.BytesToHash(emptyCodeHashBytes)
		}
		al := &accountLeaf{nonce: a.Nonce, storageRoot: sr, codeHash: ch[:]}
		al.balance.Set(&a.Balance)
		at.update(keybytesToHex(ah), al.encode())
	}
	if at.root == nil {
		return append([]byte(nil), emptyRootHash...)
	}
	return at.hash()
}

// prodRoot computes the same state's root via N42 production TrieRootComputer
// (full rebuild) — the trusted reference that eth-el's header.Root uses.
func prodRoot(t *testing.T, accts map[types.Address]*account.StateAccount, stor map[types.Address]map[types.Hash]*uint256.Int) []byte {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	r, err := trc.ComputeRoot(accts, stor)
	if err != nil {
		t.Fatalf("prod ComputeRoot: %v", err)
	}
	return r[:]
}

func addr20(i uint64) types.Address {
	var a types.Address
	binary.BigEndian.PutUint64(a[12:], i)
	return a
}

func slot32(i uint64) types.Hash {
	var s types.Hash
	binary.BigEndian.PutUint64(s[24:], i)
	return s
}

// TestAnchorEOAOnly proves my branch/extension/leaf RLP + keccak match N42
// production CalcTrieRoot exactly for a trie of plain EOAs (no storage).
func TestAnchorEOAOnly(t *testing.T) {
	rng := rand.New(rand.NewSource(2024))
	for round := 0; round < 20; round++ {
		accts := map[types.Address]*account.StateAccount{}
		n := 1 + rng.Intn(60)
		for i := 0; i < n; i++ {
			a := &account.StateAccount{}
			a.Reset()
			a.Nonce = rng.Uint64() >> uint(rng.Intn(64))
			a.Balance.SetUint64(rng.Uint64())
			a.CodeHash = types.BytesToHash(emptyCodeHashBytes)
			a.Initialised = true
			accts[addr20(uint64(round)*1000+uint64(i))] = a
		}
		want := prodRoot(t, accts, nil)
		got := buildMyAccountRoot(accts, nil)
		if !bytes.Equal(got, want) {
			t.Fatalf("round %d (n=%d): EOA-only root mismatch\n mine=%x\n prod=%x", round, n, got, want)
		}
	}
}

// TestAnchorWithStorage proves the two-level account-leaf-embeds-storageRoot
// construction matches production for contracts with storage subtrees.
func TestAnchorWithStorage(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 20; round++ {
		accts := map[types.Address]*account.StateAccount{}
		stor := map[types.Address]map[types.Hash]*uint256.Int{}
		ch := types.BytesToHash([]byte{0xcc})
		n := 1 + rng.Intn(30)
		for i := 0; i < n; i++ {
			ad := addr20(uint64(round)*1000 + uint64(i))
			a := &account.StateAccount{}
			a.Reset()
			a.Nonce = uint64(i)
			a.Balance.SetUint64(rng.Uint64())
			a.Initialised = true
			if i%2 == 0 {
				a.CodeHash = ch
				m := map[types.Hash]*uint256.Int{}
				ns := 1 + rng.Intn(15)
				for s := 0; s < ns; s++ {
					m[slot32(uint64(i)*1000+uint64(s))] = uint256.NewInt(uint64(s)*13 + 1)
				}
				stor[ad] = m
			} else {
				a.CodeHash = types.BytesToHash(emptyCodeHashBytes)
			}
			accts[ad] = a
		}
		want := prodRoot(t, accts, stor)
		got := buildMyAccountRoot(accts, stor)
		if !bytes.Equal(got, want) {
			t.Fatalf("round %d (n=%d): with-storage root mismatch\n mine=%x\n prod=%x", round, n, got, want)
		}
	}
}
