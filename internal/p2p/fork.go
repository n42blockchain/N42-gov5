package p2p

import (
	"bytes"
	"fmt"

	"github.com/holiman/uint256"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	"github.com/n42blockchain/N42/common/utils"
)

const n42ENRKey = "n42Enr"

// currentForkDigest returns the current fork digest of the node.
func (s *Service) currentForkDigest() ([4]byte, error) {
	return utils.CreateForkDigest(new(uint256.Int), s.genesisHash)
}

// compareForkENR compares fork ENRs between an incoming peer's record and our
// node's local record values.
func (s *Service) compareForkENR(record *enr.Record) error {
	remoteForkDigest := make([]byte, 4)
	entry := enr.WithEntry(n42ENRKey, &remoteForkDigest)
	if err := record.Load(entry); err != nil {
		return err
	}
	enrString, err := SerializeENR(record)
	if err != nil {
		return err
	}

	currentForkDigest, err := s.currentForkDigest()
	if err != nil {
		return errors.Wrap(err, "could not retrieve fork digest")
	}

	if !bytes.Equal(remoteForkDigest, currentForkDigest[:]) {
		return fmt.Errorf(
			"fork digest of peer with ENR %s: %v, does not match local value: %v",
			enrString,
			currentForkDigest,
			remoteForkDigest,
		)
	}
	return nil
}

// addForkEntry adds a fork digest as an ENR record entry for the local node.
func addForkEntry(node *enode.LocalNode, genesisHash types.Hash) (*enode.LocalNode, error) {
	enc, err := utils.CreateForkDigest(new(uint256.Int), genesisHash)
	if err != nil {
		return nil, err
	}
	forkEntry := enr.WithEntry(n42ENRKey, enc[:])
	node.Set(forkEntry)
	return node, nil
}
