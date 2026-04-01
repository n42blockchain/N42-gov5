package internal

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func newTestJMTCommitment() *commitment.JMTCommitment {
	store := jmt.NewMemStore()
	tree := jmt.New(store)
	return commitment.NewJMTCommitment(tree)
}

func TestStateProofDescriptorDefaultsToHashOnly(t *testing.T) {
	bc := &BlockChain{}

	desc := bc.StateProofDescriptor()
	if desc.Backend != StateProofBackendHashOnly {
		t.Fatalf("expected hash-only backend, got %q", desc.Backend)
	}
	if desc.HeaderStateRootScheme != state.RootSchemeLegacyKeccak {
		t.Fatalf("expected legacy-keccak header root scheme, got %q", desc.HeaderStateRootScheme)
	}
	if desc.ProofRootScheme != state.RootSchemeUnknown {
		t.Fatalf("expected unknown proof root scheme, got %q", desc.ProofRootScheme)
	}
	if desc.StorageHash != StorageHashSemanticsQueryTupleKeccak {
		t.Fatalf("expected query-tuple-keccak storage hash semantics, got %q", desc.StorageHash)
	}
}

func TestJMTProofDescriptorSeparatesProofAndHeaderSchemes(t *testing.T) {
	bc := &BlockChain{}
	jmtCommit := newTestJMTCommitment()

	bc.SetJMTCommitment(jmtCommit)
	desc := bc.StateProofDescriptor()
	if desc.Backend != StateProofBackendJMT {
		t.Fatalf("expected jmt backend, got %q", desc.Backend)
	}
	if desc.ProofRootScheme != state.RootSchemeJMTBlake3 {
		t.Fatalf("expected jmt proof root scheme, got %q", desc.ProofRootScheme)
	}
	if desc.HeaderStateRootScheme != state.RootSchemeLegacyKeccak {
		t.Fatalf("expected legacy header scheme before JMT block processing, got %q", desc.HeaderStateRootScheme)
	}

	bc.SetRootComputer(commitment.NewJMTRootComputer(jmtCommit))
	bc.EnableJMTForBlockProcessing()
	desc = bc.StateProofDescriptor()
	if desc.HeaderStateRootScheme != state.RootSchemeJMTBlake3 {
		t.Fatalf("expected jmt header scheme once block processing is enabled, got %q", desc.HeaderStateRootScheme)
	}
}

func TestJMTStateProofProviderStorageHashMatchesCurrentQueryTupleScheme(t *testing.T) {
	provider := NewJMTStateProofProvider(newTestJMTCommitment())
	slots := []types.Hash{
		types.HexToHash("0x1"),
		types.HexToHash("0x2"),
	}
	values := []*uint256.Int{
		uint256.NewInt(0x11),
		uint256.NewInt(0x22),
	}

	got, err := provider.StorageHash(nil, types.Address{}, slots, values, jsonrpc.BlockNumberOrHash{})
	if err != nil {
		t.Fatalf("StorageHash failed: %v", err)
	}

	var expectedData []byte
	for i, slot := range slots {
		expectedData = append(expectedData, slot.Bytes()...)
		word := values[i].Bytes32()
		expectedData = append(expectedData, word[:]...)
	}
	want := crypto.Keccak256Hash(expectedData)
	if got != want {
		t.Fatalf("unexpected storage hash: got %s want %s", got, want)
	}
}

func TestEthereumMPTStateProofProviderFailsClosed(t *testing.T) {
	provider := NewEthereumMPTStateProofProvider()
	desc := provider.Descriptor()

	if desc.Backend != StateProofBackendEthereumMPT {
		t.Fatalf("expected ethereum-mpt backend, got %q", desc.Backend)
	}
	if desc.ProofRootScheme != state.RootSchemeEthereumMPT {
		t.Fatalf("expected ethereum-mpt proof root scheme, got %q", desc.ProofRootScheme)
	}
	if desc.Semantics != StateProofSemanticsCanonicalEIP1186 {
		t.Fatalf("expected canonical-eip1186 semantics, got %q", desc.Semantics)
	}
	if desc.StorageHash != StorageHashSemanticsCanonicalTrieRoot {
		t.Fatalf("expected canonical trie-root storage hash semantics, got %q", desc.StorageHash)
	}

	if _, err := provider.AccountProof(nil, types.Address{}, jsonrpc.BlockNumberOrHash{}); !errors.Is(err, ErrCanonicalStateProofNotImplemented) {
		t.Fatalf("expected canonical-not-implemented error from AccountProof, got %v", err)
	}
	if _, err := provider.StorageProof(nil, types.Address{}, types.Hash{}, jsonrpc.BlockNumberOrHash{}); !errors.Is(err, ErrCanonicalStateProofNotImplemented) {
		t.Fatalf("expected canonical-not-implemented error from StorageProof, got %v", err)
	}
	if _, err := provider.StorageHash(nil, types.Address{}, nil, nil, jsonrpc.BlockNumberOrHash{}); !errors.Is(err, ErrCanonicalStateProofNotImplemented) {
		t.Fatalf("expected canonical-not-implemented error from StorageHash, got %v", err)
	}
}
