package block

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/stretchr/testify/require"
)

func TestBlobSidecarValidate(t *testing.T) {
	r := require.New(t)

	var nilSidecar *BlobSidecar
	r.Error(nilSidecar.Validate())

	s := &BlobSidecar{Index: MaxBlobsPerBlock, BlockHash: types.Hash{1}}
	r.Error(s.Validate())

	s2 := &BlobSidecar{Index: 0}
	r.Error(s2.Validate(), "empty block hash should be rejected")

	s3 := &BlobSidecar{Index: 0, BlockHash: types.Hash{1}}
	r.NoError(s3.Validate())
}

func TestBlobSidecarCopy(t *testing.T) {
	r := require.New(t)

	var nilSidecar *BlobSidecar
	r.Nil(nilSidecar.Copy())

	s := &BlobSidecar{
		Index:       1,
		BlockNumber: 10,
		BlockHash:   types.Hash{2},
		TxHash:      types.Hash{3},
	}
	s.CommitmentInclusionProof = [][32]byte{{0xAA}}

	cpy := s.Copy()
	r.Equal(s.Index, cpy.Index)
	r.Equal(s.BlockNumber, cpy.BlockNumber)
	r.Equal(s.BlockHash, cpy.BlockHash)
	r.Equal(s.TxHash, cpy.TxHash)
	r.Equal(s.CommitmentInclusionProof, cpy.CommitmentInclusionProof)

	// Deep copy: mutating the copy's proof slice must not affect the original.
	cpy.CommitmentInclusionProof[0][0] = 0xFF
	r.NotEqual(s.CommitmentInclusionProof[0], cpy.CommitmentInclusionProof[0])
}

func TestBlobSidecarCopyWithoutProof(t *testing.T) {
	r := require.New(t)
	s := &BlobSidecar{Index: 1}
	cpy := s.Copy()
	r.Nil(cpy.CommitmentInclusionProof)
}
