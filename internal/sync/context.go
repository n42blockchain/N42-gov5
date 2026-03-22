package sync

import (
	"io"

	"github.com/libp2p/go-libp2p/core/network"
)

// forkDigestLength specifies the fixed size of a fork digest context.
const forkDigestLength = 4

// readContextFromStream reads the fork-digest context bytes from the payload.
// Uses io.ReadFull to guarantee all 4 bytes are read, since a plain Read call
// may return fewer bytes on a streaming transport.
func readContextFromStream(stream network.Stream) ([]byte, error) {
	b := make([]byte, forkDigestLength)
	if _, err := io.ReadFull(stream, b); err != nil {
		return nil, err
	}
	return b, nil
}
