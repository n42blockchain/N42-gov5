package initialsync

import (
	"github.com/holiman/uint256"
)

// Fork recovery deliberately does NOT live here. The prysm-derived stubs that
// used to occupy this file (findFork / findForkWithPeer / findAncestor /
// nonSkippedSlotAfter, plus forkData and the queue's resetFromFork) were never
// called and returned hardcoded errors — dead code that read as a missing
// feature in every audit. They are gone on purpose: this initial-sync serves
// the HotStuff chain, where canonicality is decided solely by QC commit
// (finality — there is no longest-chain fork to scan peers for), a branch
// switch is handled by the blockchain's unwindForReimport, and a peer serving
// an orphan range trips the round-robin noProgressGuard so the node falls back
// to the consensus catch-up path instead of spinning. A beacon-style
// weak-subjectivity fork scan has no correct role in that model.

// bestFinalizedBlockNr returns the highest block number the majority of
// connected peers agree on.
func (f *blocksFetcher) bestFinalizedBlockNr() *uint256.Int {
	finalizedBlockNr, _ := f.p2p.Peers().BestPeers(f.p2p.GetConfig().MinSyncPeers, currentBlockNumber(f.chain))
	return finalizedBlockNr
}
