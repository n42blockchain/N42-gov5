package ethmpt

import (
	"bytes"
	"errors"
	"testing"

	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

func TestCanonicalProofRoundTrip(t *testing.T) {
	address := types.HexToAddress("0x1000000000000000000000000000000000000001")
	slot := types.HexToHash("0x01")
	slotValue := []byte{0x2a}

	storageTrie := gethtrie.NewEmpty(nil)
	encodedStorage, err := rlp.EncodeToBytes(slotValue)
	if err != nil {
		t.Fatalf("encode storage value: %v", err)
	}
	if err := storageTrie.Update(keccak256(slot.Bytes()), encodedStorage); err != nil {
		t.Fatalf("update storage trie: %v", err)
	}
	storageRoot := types.BytesToHash(storageTrie.Hash().Bytes())

	account := &StateAccount{
		Nonce:    7,
		Balance:  uint256.NewInt(99),
		Root:     storageRoot,
		CodeHash: emptyCodeHash.Bytes(),
	}
	accountRLP, err := rlp.EncodeToBytes(account)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}

	accountTrie := gethtrie.NewEmpty(nil)
	if err := accountTrie.Update(keccak256(address.Bytes()), accountRLP); err != nil {
		t.Fatalf("update account trie: %v", err)
	}
	stateRoot := types.BytesToHash(accountTrie.Hash().Bytes())

	accountProofHex, err := ProveAccount(&gethTrieProver{trie: accountTrie}, address)
	if err != nil {
		t.Fatalf("prove account: %v", err)
	}
	storageProofHex, err := ProveStorage(&gethTrieProver{trie: storageTrie}, slot)
	if err != nil {
		t.Fatalf("prove storage: %v", err)
	}

	accountProof, err := DecodeHexProof(accountProofHex)
	if err != nil {
		t.Fatalf("decode account proof: %v", err)
	}
	storageProof, err := DecodeHexProof(storageProofHex)
	if err != nil {
		t.Fatalf("decode storage proof: %v", err)
	}

	gotAccountRLP, err := VerifyAccountProof(stateRoot, address, accountProof)
	if err != nil {
		t.Fatalf("verify account proof: %v", err)
	}
	if !bytes.Equal(gotAccountRLP, accountRLP) {
		t.Fatalf("account leaf mismatch")
	}

	decodedAccount, err := DecodeStateAccount(gotAccountRLP)
	if err != nil {
		t.Fatalf("decode verified account: %v", err)
	}
	if decodedAccount.Nonce != account.Nonce {
		t.Fatalf("unexpected nonce: got %d want %d", decodedAccount.Nonce, account.Nonce)
	}
	if decodedAccount.Balance == nil || decodedAccount.Balance.Cmp(account.Balance) != 0 {
		t.Fatalf("unexpected balance: got %v want %v", decodedAccount.Balance, account.Balance)
	}
	if decodedAccount.StorageRoot != storageRoot {
		t.Fatalf("unexpected storage root: got %s want %s", decodedAccount.StorageRoot, storageRoot)
	}

	gotStorageRoot, err := StorageRootFromAccountRLP(gotAccountRLP)
	if err != nil {
		t.Fatalf("extract storage root: %v", err)
	}
	if gotStorageRoot != storageRoot {
		t.Fatalf("storage root mismatch: got %s want %s", gotStorageRoot, storageRoot)
	}

	gotStorageValue, err := VerifyStorageProof(storageRoot, slot, storageProof)
	if err != nil {
		t.Fatalf("verify storage proof: %v", err)
	}
	if !bytes.Equal(gotStorageValue, slotValue) {
		t.Fatalf("unexpected storage value: got %x want %x", gotStorageValue, slotValue)
	}
}

func TestDecodeHexProofRejectsBadHex(t *testing.T) {
	if _, err := DecodeHexProof([]string{"0xzz"}); err == nil {
		t.Fatal("expected bad hex proof to fail")
	}
}

type gethTrieProver struct {
	trie *gethtrie.Trie
}

func (p *gethTrieProver) Prove(key []byte, proofDb ProofWriter) error {
	if p == nil || p.trie == nil {
		return errors.New("nil geth trie prover")
	}
	return p.trie.Prove(key, &gethProofWriter{sink: proofDb})
}

type gethProofWriter struct {
	sink ProofWriter
}

func (w *gethProofWriter) Put(key []byte, value []byte) error {
	return w.sink.Put(key, value)
}

func (*gethProofWriter) Delete([]byte) error {
	return errors.New("proof writer is append-only")
}
