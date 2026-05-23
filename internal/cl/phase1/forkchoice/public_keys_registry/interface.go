//go:build n42el

package public_keys_registry

import (
	"github.com/n42blockchain/N42/internal/cl/abstract"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

// This package as a whole gets plenty of test coverage in spectests, so we can skip unit testing here.

// PublicKeyRegistry is a registry of public keys
// It is used to store public keys and their indices
type PublicKeyRegistry interface {
	ResetAnchor(s abstract.BeaconState)
	VerifyAggregateSignature(checkpoint solid.Checkpoint, pubkeysIdxs *solid.RawUint64List, message []byte, signature common.Bytes96) (bool, error)
	AddState(checkpoint solid.Checkpoint, s abstract.BeaconState)
	Prune(epoch uint64)
}
