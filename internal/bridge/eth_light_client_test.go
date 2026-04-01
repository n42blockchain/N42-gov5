package bridge

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/ethmpt"
)

func TestVerifyMPTProofAcceptsCanonicalProofs(t *testing.T) {
	accountAddr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	storageKey := types.HexToHash("0x01")
	expectedValue := []byte{0x2a}

	stateRoot, accountProof, storageProof := canonicalProofFixture(t)
	lightClient := &EthLightClient{
		verifiedRoots: map[uint64]types.Hash{
			7: stateRoot,
		},
	}

	if err := lightClient.VerifyMPTProof(7, accountAddr, storageKey, expectedValue, accountProof, storageProof); err != nil {
		t.Fatalf("VerifyMPTProof failed: %v", err)
	}
}

func TestVerifyMPTProofRejectsStorageValueMismatch(t *testing.T) {
	accountAddr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	storageKey := types.HexToHash("0x01")

	stateRoot, accountProof, storageProof := canonicalProofFixture(t)
	lightClient := &EthLightClient{
		verifiedRoots: map[uint64]types.Hash{
			9: stateRoot,
		},
	}

	err := lightClient.VerifyMPTProof(9, accountAddr, storageKey, []byte{0x2b}, accountProof, storageProof)
	if err == nil || err.Error() != "storage value mismatch" {
		t.Fatalf("expected storage value mismatch, got %v", err)
	}
}

func canonicalProofFixture(t *testing.T) (types.Hash, [][]byte, [][]byte) {
	t.Helper()

	accountProof, err := ethmpt.DecodeHexProof([]string{
		"0xf86aa1203ed02be1e351ddbcc2bf3ffafc25fb42a533df024b33c85f9805e17b60f7230cb846f8440763a0fcbdb9e7191a6bc6efbe2e1903a50bd3c79312366db1e46acf7e94788c2b4c3ea0c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
	})
	if err != nil {
		t.Fatalf("decode account proof: %v", err)
	}
	storageProof, err := ethmpt.DecodeHexProof([]string{
		"0xe3a120b10e2d527612073b26eecdfd717e6a320cf44b4afac2b0732d9fcbe2b7fa0cf62a",
	})
	if err != nil {
		t.Fatalf("decode storage proof: %v", err)
	}
	return types.HexToHash("0x206ea37a8df8a92bbe2ab21551f2b655fc364eafacdafb2cff062526cc05bc55"), accountProof, storageProof
}
