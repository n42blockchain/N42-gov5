package utils

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

func CreateForkDigest(currentBlockNr *uint256.Int, genesisValidatorsRoot types.Hash) ([4]byte, error) {
	// TODO: incorporate currentBlockNr into the fork digest.
	return ToBytes4(genesisValidatorsRoot[:]), nil
}
