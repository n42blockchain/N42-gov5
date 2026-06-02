package coldseed

import (
	"context"
	"crypto/sha256"

	"github.com/n42blockchain/N42/internal/distributed/storage/torrent"
)

// BridgeSink adapts a torrent.Bridge to the SeedSink interface: it seeds the
// content (keyed by its sha256 as the CAS content hash) and returns the hex
// infohash for the manifest. Kept in its own file so seed.go stays
// torrent-free and unit-testable with a fake sink.
type BridgeSink struct {
	Bridge *torrent.Bridge
}

func (s BridgeSink) Seed(ctx context.Context, name string, data []byte, pieceSize int) (string, error) {
	// Use sha256(data) as the CAS content-hash key (deterministic per file).
	ch := sha256.Sum256(data)
	mapping, err := s.Bridge.SeedContent(ctx, ch, name, data, pieceSize)
	if err != nil {
		return "", err
	}
	return mapping.InfoHash.HexString(), nil
}
