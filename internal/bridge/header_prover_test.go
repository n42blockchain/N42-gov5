package bridge

import (
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

func TestExtractQCFromHeaderSupportsQCAndSealLayout(t *testing.T) {
	blockHash := types.Hash{0xAA}
	qcBytes, err := (&sync_pb.HotStuffQC{
		View:      3,
		BlockHash: blockHash[:],
	}).MarshalSSZ()
	if err != nil {
		t.Fatal(err)
	}

	extra := make([]byte, 12)
	copy(extra[:4], []byte("N42H"))
	binary.LittleEndian.PutUint64(extra[4:], 4)
	extra = append(extra, qcBytes...)
	extra = append(extra, make([]byte, 96)...)

	h := &block.Header{Extra: extra}
	qc := extractQCFromHeader(h)
	if qc.View != 3 {
		t.Fatalf("qc view = %d, want 3", qc.View)
	}
	if qc.BlockHash != blockHash {
		t.Fatalf("qc block hash = %s, want %s", qc.BlockHash, blockHash)
	}
}

func TestVerifyHeaderChainLocallyAcceptsParentCommitQCLayout(t *testing.T) {
	header1 := &block.Header{
		Number: uint256.NewInt(1),
		Time:   1,
		Extra:  hotstuffExtraForTest(1, nil),
	}
	parentQC := hotstuff.QuorumCertificate{
		View:      1,
		BlockHash: header1.Hash(),
	}
	header2 := &block.Header{
		ParentHash: header1.Hash(),
		Number:     uint256.NewInt(2),
		Time:       2,
		Extra:      hotstuffExtraForTest(2, &parentQC),
	}

	err := VerifyHeaderChainLocally(
		[]*block.Header{header1, header2},
		[]hotstuff.QuorumCertificate{hotstuff.GenesisQC(), parentQC},
		nil,
	)
	if err != nil {
		t.Fatalf("VerifyHeaderChainLocally should accept parent commit QC layout: %v", err)
	}
}

func hotstuffExtraForTest(view uint64, qc *hotstuff.QuorumCertificate) []byte {
	extra := make([]byte, 12)
	copy(extra[:4], []byte("N42H"))
	binary.LittleEndian.PutUint64(extra[4:], view)
	if qc == nil {
		return extra
	}

	qcBytes, _ := (&sync_pb.HotStuffQC{
		View:      qc.View,
		BlockHash: qc.BlockHash[:],
	}).MarshalSSZ()
	extra = append(extra, qcBytes...)
	extra = append(extra, make([]byte, 96)...)
	return extra
}
