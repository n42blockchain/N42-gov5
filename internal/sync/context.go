package sync

import (
	"github.com/libp2p/go-libp2p/core/network"
)

// forkDigestLength specifies the fixed size of a fork digest context.
const forkDigestLength = 4

// readContextFromStream reads the fork-digest context bytes from the payload.
func readContextFromStream(stream network.Stream) ([]byte, error) {
	b := make([]byte, forkDigestLength)
	if _, err := stream.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}
