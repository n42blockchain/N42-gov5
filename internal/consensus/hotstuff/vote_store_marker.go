package hotstuff

import (
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// Once set, this marker makes the separate store mandatory on subsequent
// starts, even if the opt-in environment setting is removed.
var voteStoreKey = []byte("separate_vote_store_v1")

// HasIndependentVoteStore treats malformed markers as present, so recovery
// exporters and reset tools cannot bypass an independent vote journal.
func HasIndependentVoteStore(tx kv.Tx) (bool, error) {
	return tx.Has(modules.HotStuffState, voteStoreKey)
}
