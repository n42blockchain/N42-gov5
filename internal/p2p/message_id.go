package p2p

import (
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// MsgID is a content addressable ID function scoped by genesis + topic:
// `SHA256(genesisHash || topic || message.data)[:20]`.
//
// Scoping by topic is load-bearing: go-libp2p-pubsub marks a message ID seen
// GLOBALLY (across every topic) and BEFORE validation, so a bare
// SHA256(data)[:20] lets an attacker replay a block's exact bytes on any other
// subscribed topic to pre-seed the seen-cache — the genuine block then arrives
// on its real topic and is dropped as a duplicate (targeted propagation
// suppression). Folding the topic (and the fork-scoping genesis hash) in makes
// identical bytes on different topics distinct IDs, while identical bytes on
// the SAME topic still dedup as intended. This is a purely local dedup key, so
// nodes computing it differently across a version boundary stay interoperable.
func MsgID(genesisHash types.Hash, pmsg *pubsubpb.Message) string {
	topic := pmsg.GetTopic()
	combined := make([]byte, 0, len(genesisHash)+len(topic)+len(pmsg.Data))
	combined = append(combined, genesisHash[:]...)
	combined = append(combined, topic...)
	combined = append(combined, pmsg.Data...)
	h := hash.Hash(combined)
	return string(h[:20])
}
