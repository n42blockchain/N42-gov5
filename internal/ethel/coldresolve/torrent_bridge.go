package coldresolve

import (
	"time"

	"github.com/n42blockchain/N42/internal/distributed/storage/torrent"
)

// Compile-time proof that the production torrent.Bridge satisfies TorrentSource,
// so node wiring can pass a real bridge to the cold fetcher.
var _ TorrentSource = (*torrent.Bridge)(nil)

// NewTorrentFetcher builds a TorrentFetcher backed by a torrent.Bridge: cold
// segments are pulled from the swarm by infohash (1-of-N: any archive/seeder
// serving that infohash satisfies the fetch) into cacheDir.
func NewTorrentFetcher(bridge *torrent.Bridge, cacheDir string, timeout time.Duration) TorrentFetcher {
	return TorrentFetcher{Source: bridge, CacheDir: cacheDir, Timeout: timeout}
}
