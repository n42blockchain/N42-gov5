package internal

// tryZKFastPathT_test.go covers BlockChain.tryZKFastPath's guard branches:
// no body / empty ZKProof, an undecodable ZKProof payload, and — since
// NewBlockChain always wires a zkverifier.Verifier with no SP1 CLI path
// configured — the "verifier present but not cryptographically ready"
// side-check-mode branch.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/zkprover"
)

func TestTryZKFastPathNoBodyOrEmptyProof(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	blk := fx.Genesis
	require.False(t, bc.tryZKFastPath(blk))
}

func TestTryZKFastPathUndecodableProofFallsBackToEVM(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain

	blk := fx.Blocks[0]
	body, ok := blk.Body().(*block.Body)
	require.True(t, ok)
	body.ZkProof = []byte{0xde, 0xad, 0xbe, 0xef} // not a valid encoded Proof

	require.False(t, bc.tryZKFastPath(blk))
}

func TestTryZKFastPathVerifierNotCryptographicallyReady(t *testing.T) {
	fx := coreTNewChainFixture(t)
	bc := fx.Chain
	bc.SetZKProving(true)
	require.NotNil(t, bc.zkVerifier)
	require.False(t, bc.zkVerifier.CryptographicReady())

	// A syntactically valid (but not cryptographically meaningful) proof: once
	// it decodes, tryZKFastPath reaches the CryptographicReady() check and
	// returns false because the fixture's verifier has no SP1 CLI wired.
	blk := fx.Blocks[0]
	body, ok := blk.Body().(*block.Body)
	require.True(t, ok)
	encoded, err := zkprover.EncodeProof(&zkprover.Proof{
		BlockHash:   blk.Hash(),
		BlockNumber: blk.Number64().Uint64(),
		ProofData:   []byte{0x01, 0x02, 0x03},
		Type:        zkprover.ProofTypeSTARK,
	})
	require.NoError(t, err)
	body.ZkProof = encoded

	require.False(t, bc.tryZKFastPath(blk))
}
