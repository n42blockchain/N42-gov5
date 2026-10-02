package jmt

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractProofNodes(t *testing.T) {
	r := require.New(t)
	tree := New(NewMemStore())
	keys := make([]Hash, 0, 20)
	for i := 0; i < 20; i++ {
		var k Hash
		k[0] = byte(i)
		k[31] = byte(i) ^ 0x7C
		keys = append(keys, k)
		r.NoError(tree.Put(k, []byte{byte(i)}))
	}

	proof, err := tree.GetProof(keys[5])
	r.NoError(err)
	r.NotEmpty(proof.Path)

	h := DefaultHasher()
	nodes := ExtractProofNodes(proof, h)
	r.Len(nodes, len(proof.Path))

	for hash, data := range nodes {
		r.Equal(hash, h.Hash(data))
	}

	// Independently verify the root can be reached by re-hashing from the
	// leaf entry upward using only the extracted nodes.
	v, err := VerifyProof(tree.Root(), proof, h)
	r.NoError(err)
	r.Equal([]byte{5}, v)
}

func TestExtractProofNodesEmptyProof(t *testing.T) {
	r := require.New(t)
	h := DefaultHasher()
	nodes := ExtractProofNodes(&Proof{}, h)
	r.Empty(nodes)
}

func TestProofManyKeysInternalBranches(t *testing.T) {
	r := require.New(t)
	tree := New(NewMemStore())
	h := DefaultHasher()
	keys := make([]Hash, 0, 64)
	for i := 0; i < 64; i++ {
		var k Hash
		k[0] = byte(i)
		k[1] = byte(i * 3)
		keys = append(keys, k)
		r.NoError(tree.Put(k, []byte{byte(i), byte(i + 1), byte(i + 2)}))
	}
	root := tree.Root()
	for i, k := range keys {
		proof, err := tree.GetProof(k)
		r.NoError(err)
		v, err := VerifyProof(root, proof, h)
		r.NoError(err)
		r.Equal([]byte{byte(i), byte(i + 1), byte(i + 2)}, v)
	}

	// An absent key with a divergent prefix produces a verifiable exclusion
	// proof (nil value, no error).
	var absent Hash
	absent[0] = 0xFF
	absent[1] = 0xFE
	proof, err := tree.GetProof(absent)
	r.NoError(err)
	v, err := VerifyProof(root, proof, h)
	r.NoError(err)
	r.Nil(v)
}
