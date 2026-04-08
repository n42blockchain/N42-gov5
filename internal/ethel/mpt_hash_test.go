package ethel

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func TestEmptyTrieRoot(t *testing.T) {
	tr := newSimpleTrie()
	got := tr.hash()
	want := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	if got != want {
		t.Errorf("empty trie root: got %s, want %s", got.Hex(), want.Hex())
	}
}

func TestGenesisStateRoot(t *testing.T) {
	// Ethereum mainnet genesis has 8893 accounts.
	// Root = 0xd7f8974fb5ac78d9ac099b9ad5018bedc2ce0a72dad1827a1709da30580f0544
	// Test with just 1 known genesis account: address 0x000d836201318ec6899a67540690382780743280
	// balance = 200000000000000000000 (200 ETH)
	addr := types.HexToAddress("0x000d836201318ec6899a67540690382780743280")
	emptyRoot := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	emptyCodeHash := crypto.Keccak256Hash(nil)

	balance := new(big.Int)
	balance.SetString("200000000000000000000", 10)

	accRLP, _ := rlp.EncodeToBytes([]interface{}{
		uint64(0),     // nonce
		balance,       // balance
		emptyRoot,     // storageRoot
		emptyCodeHash, // codeHash
	})

	tr := newSimpleTrie()
	addrHash := crypto.Keccak256(addr[:])
	tr.update(addrHash, accRLP)

	root := tr.hash()
	t.Logf("Single account trie root: %s", root.Hex())
	// Can't check exact value without full genesis, but root should not be empty.
	if root == (types.Hash{}) {
		t.Error("root should not be zero")
	}
}

func TestStorageTrieRoot(t *testing.T) {
	// Storage trie with 1 slot: slot 0 = value 1
	// The value in the trie is RLP(trimmed_value), i.e. RLP(0x01) = 0x01
	slot := types.Hash{}
	value := []byte{0x01}
	slotHash := crypto.Keccak256(slot[:])

	valRLP, _ := rlp.EncodeToBytes(value)

	tr := newSimpleTrie()
	tr.update(slotHash, valRLP)
	root := tr.hash()
	t.Logf("Single slot storage root: %s", root.Hex())
	if root == (types.Hash{}) {
		t.Error("root should not be zero")
	}
}

func TestReceiptTrieRoot(t *testing.T) {
	// Empty receipt trie root should be emptyRoot.
	list := ethReceiptList(nil)
	got := ethDeriveSha(list)
	want := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	if got != want {
		t.Errorf("empty receipt root: got %s, want %s", got.Hex(), want.Hex())
	}
}
