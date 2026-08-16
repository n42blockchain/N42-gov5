package eldevp2p

import (
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// EngineBlockKnownFunc reports whether Engine already has a block/header.
type EngineBlockKnownFunc func(types.Hash) bool

// EngineBlockImporter validates a Paris block through Engine newPayload.
type EngineBlockImporter func(*block.Block) (status string, latestValidHash *types.Hash, err error)
