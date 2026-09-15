package p2p

import (
	"io"

	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// MsgID is a content addressable ID function scoped by genesis + topic:
// `Keccak256(genesisHash || topic || message.data)[:20]`.
//
// Scoping by topic is load-bearing: go-libp2p-pubsub marks a message ID seen
// GLOBALLY (across every topic) and BEFORE validation, so a bare
// Keccak256(data)[:20] lets an attacker replay a block's exact bytes on any
// other subscribed topic to pre-seed the seen-cache — the genuine block then
// arrives on its real topic and is dropped as a duplicate (targeted propagation
// suppression). Folding the topic (and the fork-scoping genesis hash) in makes
// identical bytes on different topics distinct IDs, while identical bytes on
// the SAME topic still dedup as intended. This is a purely local dedup key, so
// nodes computing it differently across a version boundary stay interoperable.
//
// The three parts are fed to the hasher in turn rather than concatenated first:
// pubsub calls this for every received message before validation, and a block
// gossip message is ~18 MB at 163k transactions, so the concatenation was an
// 18 MB allocation per copy received (7.1 GB of a follower's allocations over a
// seven-node round, 35zzo). The ID is byte-identical.
func MsgID(genesisHash types.Hash, pmsg *pubsubpb.Message) string {
	h, _ := hash.HasherPool.Get().(crypto.KeccakState)
	defer hash.HasherPool.Put(h)
	h.Reset()
	// #nosec G104 -- hash.Hash writes never return an error.
	h.Write(genesisHash[:])
	// #nosec G104
	io.WriteString(h, pmsg.GetTopic())
	// #nosec G104
	h.Write(pmsg.Data)
	var b [32]byte
	// #nosec G104
	h.Read(b[:])
	return string(b[:20])
}
