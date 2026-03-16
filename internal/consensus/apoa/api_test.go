package apoa

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

type apiChainStub struct {
	consensus.ConsensusChainReader
	current      block.IBlock
	headerByHash block.IHeader
}

func (s *apiChainStub) CurrentBlock() block.IBlock {
	return s.current
}

func (s *apiChainStub) GetHeaderByHash(types.Hash) (block.IHeader, error) {
	return s.headerByHash, nil
}

func TestGetSnapshotRejectsMissingCurrentBlock(t *testing.T) {
	api := &API{
		chain: &apiChainStub{},
	}

	_, err := api.GetSnapshot(nil)
	if !errors.Is(err, errUnknownBlock) {
		t.Fatalf("GetSnapshot() error = %v", err)
	}
}

func TestGetSnapshotAtHashRejectsNilHeaderNumber(t *testing.T) {
	api := &API{
		chain: &apiChainStub{
			headerByHash: &block.Header{},
		},
	}

	_, err := api.GetSnapshotAtHash(types.Hash{})
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("GetSnapshotAtHash() error = %v", err)
	}
}

func TestRequireHeaderNumberRejectsNilNumber(t *testing.T) {
	_, err := requireHeaderNumber(&block.Header{}, "header number unavailable")
	if err == nil || err.Error() != "header number unavailable" {
		t.Fatalf("requireHeaderNumber() error = %v", err)
	}
}
