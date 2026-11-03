package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

var signingMessageSink []byte

func BenchmarkH2V4SigningMessage(b *testing.B) {
	id := H2V4ChainIdentity{ChainID: 42, GenesisHash: types.Hash{1}}
	hash, changes := types.Hash{2}, types.Hash{3}
	for _, tc := range []struct {
		name string
		make func() []byte
	}{
		{"proposal", func() []byte { return H2V4ProposalSigningMessage(id, 7, hash, changes) }},
		{"vote", func() []byte { return H2V4VoteSigningMessage(id, 7, hash) }},
		{"commit", func() []byte { return H2V4CommitSigningMessage(id, 7, hash, changes) }},
		{"timeout", func() []byte { return H2V4TimeoutSigningMessage(id, 7) }},
		{"new-view", func() []byte { return H2V4NewViewSigningMessage(id, 7) }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				signingMessageSink = tc.make()
			}
		})
	}
}
