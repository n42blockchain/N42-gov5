package coldseed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/n42blockchain/N42/internal/ethel/historyexpiry"
	"github.com/n42blockchain/N42/internal/sync/torrentsync"
	"github.com/n42blockchain/N42/log"
)

const segSize = historyexpiry.SegSize

// SeedSink publishes one file's bytes to the swarm and returns its hex infohash.
// Decouples coldseed from the torrent client (BridgeSink adapts torrent.Bridge),
// keeping the seeding logic unit-testable without a network.
type SeedSink interface {
	Seed(ctx context.Context, name string, data []byte, pieceSize int) (infohash string, err error)
}

// Options configures a seeding pass / service.
type Options struct {
	Dir          string        // freezer dir with <prefix>.NNNN.cdat
	Prefixes     []string      // e.g. ["bodyc","headerc","witness"]
	ManifestPath string        // read existing + write updated manifest
	ChainID      uint64        // for the manifest
	PieceSize    int           // torrent piece size
	Interval     time.Duration // 0 = single pass; >0 = re-seed loop (active file tracks the tail)
}

// parseFileNum extracts NNNN from "<prefix>.NNNN.cdat".
func parseFileNum(name, prefix string) (int, bool) {
	if !strings.HasPrefix(name, prefix+".") || !strings.HasSuffix(name, ".cdat") {
		return 0, false
	}
	mid := strings.TrimSuffix(strings.TrimPrefix(name, prefix+"."), ".cdat")
	n, err := strconv.Atoi(mid)
	if err != nil {
		return 0, false
	}
	return n, true
}

// cidxGroups maps fileNum → [minSeg,maxSeg] from <prefix>.cidx (8B/seg, fileNum
// = LE u16[0:2]). Returns nil if absent (block ranges then 0).
func cidxGroups(dir, prefix string) map[int][2]uint64 {
	data, err := os.ReadFile(filepath.Join(dir, prefix+".cidx"))
	if err != nil {
		return nil
	}
	// Only the ethel columnar cidx is 8 bytes/segment with a small segment count.
	// Freezer-table indexes (e.g. receipts.cidx, senders.cidx) are per-block and
	// huge — parsing them as 8B/seg would yield bogus ranges, so skip them: the
	// files are still seedable, just without manifest block ranges.
	if len(data)%8 != 0 || len(data)/8 > 1_000_000 {
		return nil
	}
	groups := map[int][2]uint64{}
	for s := 0; (s+1)*8 <= len(data); s++ {
		fn := int(uint16(data[s*8]) | uint16(data[s*8+1])<<8)
		g, ok := groups[fn]
		if !ok {
			groups[fn] = [2]uint64{uint64(s), uint64(s)}
		} else {
			if uint64(s) < g[0] {
				g[0] = uint64(s)
			}
			if uint64(s) > g[1] {
				g[1] = uint64(s)
			}
			groups[fn] = g
		}
	}
	return groups
}

// ScanPrefix returns the current FileState list for a prefix (active = max NNNN).
func ScanPrefix(dir, prefix string) ([]FileState, error) {
	matches, err := filepath.Glob(filepath.Join(dir, prefix+".*.cdat"))
	if err != nil {
		return nil, err
	}
	type fe struct {
		num  int
		name string
		size int64
	}
	var fes []fe
	for _, m := range matches {
		name := filepath.Base(m)
		num, ok := parseFileNum(name, prefix)
		if !ok {
			continue
		}
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		fes = append(fes, fe{num, name, fi.Size()})
	}
	if len(fes) == 0 {
		return nil, nil
	}
	sort.Slice(fes, func(i, j int) bool { return fes[i].num < fes[j].num })
	maxNum := fes[len(fes)-1].num
	groups := cidxGroups(dir, prefix)
	out := make([]FileState, 0, len(fes))
	for _, f := range fes {
		fs := FileState{FileName: f.name, Size: f.size, Active: f.num == maxNum}
		if g, ok := groups[f.num]; ok {
			fs.FromBlock = g[0] * segSize
			fs.ToBlock = g[1]*segSize + segSize - 1
		}
		out = append(out, fs)
	}
	return out, nil
}

// RunOnce performs a single seeding pass: scan each prefix, plan, (re)seed the
// new/changed sealed files + every active file via sink, and write the merged
// manifest. Idempotent for unchanged sealed files (skipped).
func RunOnce(ctx context.Context, opts Options, sink SeedSink) error {
	existing, _ := torrentsync.LoadManifest(opts.ManifestPath) // nil on first run
	var allKeep, allSeeded []torrentsync.SegmentInfo
	for _, prefix := range opts.Prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		current, err := ScanPrefix(opts.Dir, prefix)
		if err != nil {
			return fmt.Errorf("scan %s: %w", prefix, err)
		}
		if len(current) == 0 {
			continue
		}
		plan := PlanSeed(current, existing)
		allKeep = append(allKeep, plan.Keep...)
		log.Info("coldseed: prefix scanned", "prefix", prefix, "files", len(current),
			"kept", len(plan.Keep), "reseed", len(plan.Reseed), "active", current[len(current)-1].FileName)
		for _, f := range plan.Reseed {
			data, err := os.ReadFile(filepath.Join(opts.Dir, f.FileName))
			if err != nil {
				return fmt.Errorf("read %s: %w", f.FileName, err)
			}
			sum := sha256.Sum256(data)
			ih, err := sink.Seed(ctx, f.FileName, data, opts.PieceSize)
			if err != nil {
				return fmt.Errorf("seed %s: %w", f.FileName, err)
			}
			allSeeded = append(allSeeded, torrentsync.SegmentInfo{
				FromBlock: f.FromBlock, ToBlock: f.ToBlock, FileName: f.FileName,
				Size: f.Size, BlockCount: f.ToBlock - f.FromBlock + 1,
				SHA256: hex.EncodeToString(sum[:]), InfoHash: ih,
			})
		}
	}
	segs := append(allKeep, allSeeded...)
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].FileName != segs[j].FileName {
			return segs[i].FileName < segs[j].FileName
		}
		return segs[i].FromBlock < segs[j].FromBlock
	})
	m := &torrentsync.Manifest{ChainID: opts.ChainID, Segments: segs, UpdatedAt: time.Unix(0, 0)}
	if err := torrentsync.SaveManifest(m, opts.ManifestPath); err != nil {
		return fmt.Errorf("save manifest: %w", err)
	}
	log.Info("coldseed: manifest written", "path", opts.ManifestPath,
		"segments", len(segs), "kept", len(allKeep), "seeded", len(allSeeded))
	return nil
}

// Service runs RunOnce immediately and then every Interval (if >0), so the
// active (growing) file is periodically re-seeded. Implements ethel.Service.
type Service struct {
	opts   Options
	sink   SeedSink
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService builds a periodic seeder service.
func NewService(opts Options, sink SeedSink) *Service {
	return &Service{opts: opts, sink: sink, done: make(chan struct{})}
}

func (s *Service) Name() string { return "history-seeder" }

func (s *Service) Start(ctx context.Context) error {
	ctx, s.cancel = context.WithCancel(ctx)
	go func() {
		defer close(s.done)
		for {
			if err := RunOnce(ctx, s.opts, s.sink); err != nil {
				log.Warn("coldseed: pass error", "err", err)
			}
			if s.opts.Interval <= 0 {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.opts.Interval):
			}
		}
	}()
	return nil
}

func (s *Service) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}
