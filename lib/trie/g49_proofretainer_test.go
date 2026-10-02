package trie_test

// g49: exercises trie.ProofRetainer end to end — NewProofRetainer,
// ProofElement, ProofResult — by reproducing the exact production
// pattern used by cmd/n42-stateless-realproof/main.go: seed
// HashedAccounts + HashedStorage, drive FlatDBTrieLoader.CalcTrieRoot
// with SetProofRetainer attached, then verify the resulting EIP-1186
// proof with an INDEPENDENT verifier
// (internal/ethel/stateless.VerifyAccountInclusion, which rebuilds a
// partial trie from the proof bytes and checks it anchors to the
// computed root) — this exercises the hashbuilder proof-element
// plumbing (setProofElement/leaf/accountLeaf/branch/extension) that
// plain CalcTrieRoot (no retainer) never touches.

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/trie"
)

// g49SeedOneAccountWithStorageAndNoise writes ONE target account (with
// a handful of storage slots) plus `noise` other accounts (to force
// the trie to actually branch instead of being a single-leaf trie),
// and returns the target's encoded account + addr + storage slots.
func g49SeedOneAccountWithStorageAndNoise(t *testing.T, tx kv.RwTx, noise int) (addr types.Address, enc []byte, slots []types.Hash) {
	t.Helper()
	addr = types.Address{0xAB, 0xCD, 0xEF, 0x01}
	addrHash, err := types.HashData(addr[:])
	if err != nil {
		t.Fatal(err)
	}

	a := account.NewAccount()
	a.Nonce = 7
	a.Balance = *uint256.NewInt(123456789)
	a.Initialised = true

	nSlots := 4
	slots = make([]types.Hash, nSlots)
	for j := 0; j < nSlots; j++ {
		var raw [32]byte
		raw[31] = byte(j + 1)
		slots[j] = types.Hash(raw)
		slotHash, herr := types.HashData(slots[j][:])
		if herr != nil {
			t.Fatal(herr)
		}
		val := []byte{byte(j + 1), 0x42}
		fullKey := append(append([]byte{}, addrHash[:]...), slotHash[:]...)
		if err := tx.Put(kv.HashedStorage, fullKey, val); err != nil {
			t.Fatal(err)
		}
	}

	buf := make([]byte, a.EncodingLengthForStorage())
	a.EncodeForStorage(buf)
	if err := tx.Put(kv.HashedAccounts, addrHash[:], buf); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < noise; i++ {
		var seed [8]byte
		binary.BigEndian.PutUint64(seed[:], uint64(i)*2654435761+1)
		h := crypto.Keccak256(seed[:])
		na := account.NewAccount()
		na.Nonce = uint64(i) + 1
		na.Balance = *uint256.NewInt(uint64(i) + 1)
		nbuf := make([]byte, na.EncodingLengthForStorage())
		na.EncodeForStorage(nbuf)
		if err := tx.Put(kv.HashedAccounts, h, nbuf); err != nil {
			t.Fatal(err)
		}
	}

	// Re-read the just-written encoding via DecodeForStorage, exactly
	// as the production CLI tool does, so Initialised and friends are
	// set the same way the real read path sets them.
	raw, err := tx.GetOne(kv.HashedAccounts, addrHash[:])
	if err != nil {
		t.Fatal(err)
	}
	var decoded account.StateAccount
	if err := decoded.DecodeForStorage(raw); err != nil {
		t.Fatal(err)
	}
	return addr, raw, slots
}

func TestProofRetainer_AccountAndStorageProof_VerifiesIndependently(t *testing.T) {
	db := memdb.New(t.TempDir())
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	addr, enc, slots := g49SeedOneAccountWithStorageAndNoise(t, tx, 50)

	var acc account.StateAccount
	if err := acc.DecodeForStorage(enc); err != nil {
		t.Fatal(err)
	}

	rl := trie.NewRetainList(0)
	pr, err := trie.NewProofRetainer(addr, &acc, slots, rl)
	if err != nil {
		t.Fatalf("NewProofRetainer: %v", err)
	}

	loader := trie.NewFlatDBTrieLoader("g49-proofretainer", rl, nil, nil, false)
	loader.SetProofRetainer(pr)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		t.Fatalf("CalcTrieRoot: %v", err)
	}

	res, err := pr.ProofResult()
	if err != nil {
		t.Fatalf("ProofResult: %v", err)
	}
	if res.Address != addr {
		t.Errorf("ProofResult.Address: got %x want %x", res.Address, addr)
	}
	if res.Nonce != acc.Nonce {
		t.Errorf("ProofResult.Nonce: got %d want %d", res.Nonce, acc.Nonce)
	}
	if len(res.AccountProof) == 0 {
		t.Fatal("expected non-empty AccountProof")
	}
	if len(res.StorageProof) != len(slots) {
		t.Fatalf("expected %d storage proofs, got %d", len(slots), len(res.StorageProof))
	}

	// Independent verifier: rebuilds a partial trie purely from the
	// proof bytes and checks it anchors to `root`. This is a
	// completely separate code path from ProofRetainer/HashBuilder.
	va, err := stateless.VerifyAccountInclusion(root[:], res)
	if err != nil {
		t.Fatalf("VerifyAccountInclusion: %v", err)
	}
	if va.Nonce != acc.Nonce {
		t.Errorf("verified nonce: got %d want %d", va.Nonce, acc.Nonce)
	}

	nonEmptyStorage := 0
	for _, sp := range res.StorageProof {
		if sp.Value != "0" {
			nonEmptyStorage++
		}
	}
	if nonEmptyStorage != len(slots) {
		t.Errorf("expected all %d slots to have non-zero proven values, got %d", len(slots), nonEmptyStorage)
	}
}
