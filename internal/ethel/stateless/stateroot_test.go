package stateless

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// --- ground-truth two-level trie builder -------------------------------------

type acctState struct {
	nonce    uint64
	balance  uint64
	codeHash []byte            // 32B
	storage  map[string][]byte // slotHash(32B string) -> raw 32B value (untrimmed)
}

func storRoot(st map[string][]byte) ([]byte, *partialTrie) {
	t := fullTrie()
	any := false
	for sh, v := range st {
		tv := trimLeftZeros(v)
		if len(tv) == 0 {
			continue
		}
		any = true
		_ = t.update(keybytesToHex([]byte(sh)), rlpStr(tv))
	}
	if !any {
		return append([]byte(nil), emptyRootHash...), t
	}
	return t.hash(), t
}

func buildAccountTrie(world map[string]*acctState) (root []byte, at *partialTrie, stTries map[string]*partialTrie) {
	at = fullTrie()
	stTries = map[string]*partialTrie{}
	for ah, a := range world {
		sr, st := storRoot(a.storage)
		stTries[ah] = st
		al := &accountLeaf{nonce: a.nonce, storageRoot: sr, codeHash: a.codeHash}
		al.balance.SetUint64(a.balance)
		_ = at.update(keybytesToHex([]byte(ah)), al.encode())
	}
	return at.hash(), at, stTries
}

func ahash(i uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], i)
	return string(keccak(b[:]))
}

func shash(i uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], i^0xa5a5a5a5)
	return keccak(b[:])
}

func val32(i uint64) []byte {
	b := make([]byte, 32)
	binary.BigEndian.PutUint64(b[24:], i)
	return b
}

func sortedAddrs(world map[string]*acctState) []string {
	ks := make([]string, 0, len(world))
	for k := range world {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// serializeAccountProof returns the full account-trie node set as a proof.
func serializeAccountProof(at *partialTrie) [][]byte {
	var p [][]byte
	collectNodes(at.root, &p)
	if e := encodeNode(at.root); len(e) < 32 {
		p = append(p, e)
	}
	return p
}

func serializeStorageProof(st *partialTrie) [][]byte {
	var p [][]byte
	collectNodes(st.root, &p)
	if e := encodeNode(st.root); len(e) < 32 {
		p = append(p, e)
	}
	return p
}

// TestTwoLevelStateRoot drives the two-level updater deterministically (sorted
// account order, fixed case assignment) and checks Root() == full rebuild.
func TestTwoLevelStateRoot(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	for round := 0; round < 60; round++ {
		world := map[string]*acctState{}
		nAcct := 10 + rng.Intn(40)
		ch := make([]byte, 32)
		ch[0] = 0xcc
		for i := 0; i < nAcct; i++ {
			a := &acctState{
				nonce:    uint64(i),
				balance:  uint64(rng.Intn(1 << 30)),
				codeHash: append([]byte(nil), emptyCodeHashBytes...),
				storage:  map[string][]byte{},
			}
			if i%2 == 0 {
				a.codeHash = append([]byte(nil), ch...)
				nslot := 1 + rng.Intn(20)
				for s := 0; s < nslot; s++ {
					a.storage[string(shash(uint64(i)*1000+uint64(s)))] = val32(uint64(s)*7 + 1)
				}
			}
			world[ahash(uint64(round)*100000+uint64(i))] = a
		}

		baseRoot, accTrie, stTries := buildAccountTrie(world)
		u, err := NewStateRootUpdater(baseRoot, serializeAccountProof(accTrie))
		if err != nil {
			t.Fatalf("round %d: NewStateRootUpdater: %v", round, err)
		}
		if !bytes.Equal(u.acct.hash(), baseRoot) {
			t.Fatalf("round %d: pre-root mismatch", round)
		}

		for idx, ah := range sortedAddrs(world) {
			a := world[ah]
			var ahA types.Hash
			copy(ahA[:], ah)
			switch idx % 4 {
			case 0:
				a.balance += 777
				e := &AccountEdit{Nonce: a.nonce, CodeHash: a.codeHash}
				e.Balance.SetUint64(a.balance)
				u.SetAccount(ahA, e)
			case 1:
				if len(a.storage) > 0 {
					sr, _ := storRoot(a.storage)
					if err := u.AddStorageProof(ahA, sr, serializeStorageProof(stTries[ah])); err != nil {
						t.Fatalf("round %d: AddStorageProof: %v", round, err)
					}
					ns := shash(uint64(round)*7777 + uint64(idx))
					a.storage[string(ns)] = val32(uint64(idx) + 5)
					var nsA types.Hash
					copy(nsA[:], ns)
					if err := u.SetStorage(ahA, nsA, val32(uint64(idx)+5)); err != nil {
						t.Fatalf("round %d: SetStorage: %v", round, err)
					}
				}
			case 2:
				a.nonce++
				e := &AccountEdit{Nonce: a.nonce, CodeHash: a.codeHash}
				e.Balance.SetUint64(a.balance)
				u.SetAccount(ahA, e)
			case 3:
				// no-op
			}
		}

		wantRoot, _, _ := buildAccountTrie(world)
		got, err := u.Root()
		if err != nil {
			t.Fatalf("round %d: Root: %v", round, err)
		}
		if !bytes.Equal(got, wantRoot) {
			t.Fatalf("round %d (nAcct=%d): stateless %x != rebuild %x", round, nAcct, got[:8], wantRoot[:8])
		}
	}
}
