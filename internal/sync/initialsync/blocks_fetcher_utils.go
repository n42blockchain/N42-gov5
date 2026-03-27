package initialsync

import (
	"context"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/pkg/errors"
	"go.opencensus.io/trace"

	"github.com/n42blockchain/N42/proto/types_pb"
)

// forkData represents alternative chain path supported by a given peer.
// Blocks are stored in an ascending slot order. The first block is guaranteed to have parent
// either in DB or initial sync cache.
type forkData struct {
	peer   peer.ID
	blocks []*types_pb.Block
}

// nonSkippedSlotAfter is not implemented - returns nil.
func (f *blocksFetcher) nonSkippedSlotAfter(ctx context.Context, blockNr *uint256.Int) (*uint256.Int, error) {
	ctx, span := trace.StartSpan(ctx, "initialsync.nonSkippedSlotAfter")
	defer span.End()
	return nil, nil
}

// findFork is not yet implemented - returns errNoPeersWithAltBlocks.
func (f *blocksFetcher) findFork(ctx context.Context, blockNr *uint256.Int) (*forkData, error) {
	ctx, span := trace.StartSpan(ctx, "initialsync.findFork")
	defer span.End()

	return nil, errNoPeersWithAltBlocks
}

// findForkWithPeer is not yet implemented -- returns error unconditionally.
func (f *blocksFetcher) findForkWithPeer(ctx context.Context, pid peer.ID, blockNr *uint256.Int) (*forkData, error) {
	return nil, errors.New("no alternative blocks exist within scanned range")
}

// findAncestor is not yet implemented -- returns error unconditionally.
func (f *blocksFetcher) findAncestor(ctx context.Context, pid peer.ID, b *types_pb.Block) (*forkData, error) {
	return nil, errors.New("no common ancestor found")
}

// bestFinalizedSlot returns the highest finalized slot of the majority of connected peers.
func (f *blocksFetcher) bestFinalizedBlockNr() *uint256.Int {
	finalizedBlockNr, _ := f.p2p.Peers().BestPeers(f.p2p.GetConfig().MinSyncPeers, currentBlockNumber(f.chain))
	return finalizedBlockNr
}
