package coldresolve

import (
	"context"
	"time"

	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/log"
)

// DownloadAll eagerly fetches EVERY segment in the manifest — the archive-node
// path: pull the full history (headers + bodies + witness + receipts) from the
// swarm under 1-of-N. fetcher.Fetch is idempotent (a segment already present in
// the cache/store is skipped), so re-runs are cheap. With verifySHA256 set,
// each fetched file is checked against the manifest hash. Returns counts.
func DownloadAll(m *torrentsync.Manifest, f Fetcher, verifySHA256 bool) (fetched, skipped, failed int) {
	for i := range m.Segments {
		seg := m.Segments[i]
		p, err := f.Fetch(seg)
		if err != nil {
			failed++
			log.Warn("coldresolve: download failed", "file", seg.FileName, "err", err)
			continue
		}
		if verifySHA256 && seg.SHA256 != "" {
			if sum, err := sha256File(p); err != nil || sum != seg.SHA256 {
				failed++
				log.Warn("coldresolve: sha256 mismatch", "file", seg.FileName)
				continue
			}
		}
		fetched++
	}
	return fetched, skipped, failed
}

// sha256File is shared with resolver.go (same package).

// DownloadService runs DownloadAll once at Start (in the background) so an
// archive node fills its full history from the manifest at boot. Implements
// ethel.Service (Name/Start/Stop).
type DownloadService struct {
	m      *torrentsync.Manifest
	f      Fetcher
	verify bool
	cancel context.CancelFunc
}

// NewDownloadService builds the archive full-history downloader.
func NewDownloadService(m *torrentsync.Manifest, f Fetcher, verifySHA256 bool) *DownloadService {
	return &DownloadService{m: m, f: f, verify: verifySHA256}
}

func (s *DownloadService) Name() string { return "history-download" }

func (s *DownloadService) Start(ctx context.Context) error {
	_, s.cancel = context.WithCancel(ctx)
	go func() {
		t0 := time.Now()
		log.Info("coldresolve: archive full-history download starting", "segments", len(s.m.Segments))
		fetched, _, failed := DownloadAll(s.m, s.f, s.verify)
		log.Info("coldresolve: archive full-history download done",
			"fetched", fetched, "failed", failed, "elapsed", time.Since(t0).Truncate(time.Second))
	}()
	return nil
}

func (s *DownloadService) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}
