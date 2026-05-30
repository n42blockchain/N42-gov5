package stateless

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// buildBlockProof constructs, from a base world and a mutation, the BlockProof a
// producer would emit: the account-trie RLP multiproof at preRoot, and per-
// changed-contract storage RLP proofs. Returns preRoot, postRoot (full rebuild
// of the mutated world), and the BlockProof. mut mutates `base` in place and
// returns the change list.
//
// For test simplicity the "proof" is the FULL trie node set (serialize*Proof); a
// real producer emits only changed paths + boundary hashes, but the verifier is
// identical either way (boundary nodes become hashNodes resolved from the set).
func buildBlockProof(t *testing.T, base map[string]*acctState, mut func(map[string]*acctState) []AccountChange) (pre, post []byte, bp *BlockProof) {
	t.Helper()
	preRoot, accTrie, stTries := buildAccountTrie(base)
	accProof := serializeAccountProof(accTrie)

	// snapshot pre-state storageRoots + storage proofs per contract
	preStorRoot := map[string][]byte{}
	storProof := map[string][][]byte{}
	for ah, a := range base {
		if len(a.storage) == 0 {
			continue
		}
		sr, _ := storRoot(a.storage)
		preStorRoot[ah] = sr
		storProof[ah] = serializeStorageProof(stTries[ah])
	}

	changes := mut(base) // mutates base in place, returns change list
	for i := range changes {
		ah := string(changes[i].AddrHash[:])
		if len(changes[i].Storage) > 0 {
			changes[i].StorageRoot = preStorRoot[ah]
			changes[i].StorageProof = storProof[ah]
		}
	}
	postRoot, _, _ := buildAccountTrie(base)
	return preRoot, postRoot, &BlockProof{Number: 100, AccountProof: accProof, Changes: changes}
}

func mkContractWorld(round, n int) map[string]*acctState {
	world := map[string]*acctState{}
	ch := make([]byte, 32)
	ch[0] = 0xcc
	for i := 0; i < n; i++ {
		a := &acctState{nonce: uint64(i), balance: 100 + uint64(i),
			codeHash: append([]byte(nil), emptyCodeHashBytes...), storage: map[string][]byte{}}
		if i%2 == 0 {
			a.codeHash = append([]byte(nil), ch...)
			for s := 0; s < 4; s++ {
				a.storage[string(shash(uint64(i)*1000+uint64(s)))] = val32(uint64(s)*7 + 1)
			}
		}
		world[ahash(uint64(round)*100000+uint64(i))] = a
	}
	return world
}

// TestVerifyStateRootPipeline: account proof + per-contract storage proofs +
// changeset → VerifyStateRoot passes for the honest postRoot and fails for a
// tampered post or pre root.
func TestVerifyStateRootPipeline(t *testing.T) {
	for round := 0; round < 20; round++ {
		base := mkContractWorld(round, 16)
		pre, post, bp := buildBlockProof(t, base, func(w map[string]*acctState) []AccountChange {
			var changes []AccountChange
			idx := 0
			for _, ah := range sortedAddrs(w) {
				a := w[ah]
				var ahA types.Hash
				copy(ahA[:], ah)
				if idx%2 == 0 && len(a.storage) > 0 {
					ns := shash(uint64(round)*99999 + uint64(idx))
					a.storage[string(ns)] = val32(uint64(idx) + 3)
					var slot types.Hash
					copy(slot[:], ns)
					c := AccountChange{AddrHash: ahA, Nonce: a.nonce, CodeHash: a.codeHash,
						Storage: []StorageChange{{SlotHash: slot, Value: val32(uint64(idx) + 3)}}}
					c.Balance.SetUint64(a.balance)
					changes = append(changes, c)
				} else {
					a.balance += 555
					c := AccountChange{AddrHash: ahA, Nonce: a.nonce, CodeHash: a.codeHash}
					c.Balance.SetUint64(a.balance)
					changes = append(changes, c)
				}
				idx++
			}
			return changes
		})

		if err := VerifyStateRoot(pre, post, bp); err != nil {
			t.Fatalf("round %d: honest verify failed: %v", round, err)
		}
		bad := append([]byte(nil), post...)
		bad[0] ^= 0xff
		if err := VerifyStateRoot(pre, bad, bp); err == nil {
			t.Fatalf("round %d: tampered postRoot accepted", round)
		}
		badPre := append([]byte(nil), pre...)
		badPre[0] ^= 0xff
		if err := VerifyStateRoot(badPre, post, bp); err == nil {
			t.Fatalf("round %d: tampered preRoot accepted", round)
		}
	}
}

// TestVerifyAgainstChain glues HeaderChain → VerifyStateRoot.
func TestVerifyAgainstChain(t *testing.T) {
	base := mkContractWorld(7, 12)
	pre, post, bp := buildBlockProof(t, base, func(w map[string]*acctState) []AccountChange {
		var changes []AccountChange
		for _, ah := range sortedAddrs(w) {
			a := w[ah]
			a.balance += 999
			var ahA types.Hash
			copy(ahA[:], ah)
			c := AccountChange{AddrHash: ahA, Nonce: a.nonce, CodeHash: a.codeHash}
			c.Balance.SetUint64(a.balance)
			changes = append(changes, c)
		}
		return changes
	})

	anchor := mkHeader(99, types.Hash{0x1})
	copy(anchor.Root[:], pre)
	hc, err := NewHeaderChain(anchor)
	if err != nil {
		t.Fatal(err)
	}
	child := mkHeader(100, anchor.Hash())
	copy(child.Root[:], post)
	if err := hc.Extend(child); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgainstChain(hc, bp); err != nil {
		t.Fatalf("VerifyAgainstChain: %v", err)
	}
}

var _ = uint256.Int{}
