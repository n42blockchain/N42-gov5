//go:build n42el

package checkpoint_sync

import (
	"context"

	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
)

type CheckpointSyncer interface {
	GetLatestBeaconState(ctx context.Context) (*state.CachingBeaconState, error)
}
