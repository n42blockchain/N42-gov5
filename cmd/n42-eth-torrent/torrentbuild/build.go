// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Package torrentbuild builds ONE multi-file BitTorrent metainfo over the file
// set named by a tier manifest, streaming the piece hashes from disk so an
// 800 GB archive never has to fit in RAM. The infohash depends only on the info
// dict (name, piece length, ordered files, piece hashes) — trackers, webseeds,
// comment, and creation date live OUTSIDE it, so the same datadir yields a
// reproducible infohash across producers regardless of announce config.

package torrentbuild

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

const (
	// piece-length bounds. Below 256 KiB the .torrent's piece list bloats; above
	// 16 MiB hurts swarm granularity (a single bad piece re-downloads a lot).
	minPieceLen int64 = 256 << 10 // 256 KiB
	maxPieceLen int64 = 16 << 20  // 16 MiB
	// target piece COUNT band — auto-sizing aims here, then clamps to the bounds.
	targetPieceLo = 20_000
	targetPieceHi = 60_000
)

// AutoPieceLength picks a power-of-two piece length so the torrent has a sane
// number of pieces (~20k–60k) for the given total size, clamped to [256KiB,16MiB].
// Power-of-two keeps client compatibility maximal.
func AutoPieceLength(total int64) int64 {
	if total <= 0 {
		return minPieceLen
	}
	p := minPieceLen
	for p < maxPieceLen && total/p > targetPieceHi {
		p <<= 1
	}
	// If even max pieces is too coarse (huge archive), maxPieceLen stands; if the
	// archive is tiny, ensure we don't exceed it with an oversized piece.
	for p > minPieceLen && total/p < targetPieceLo {
		p >>= 1
	}
	if p < minPieceLen {
		p = minPieceLen
	}
	if p > maxPieceLen {
		p = maxPieceLen
	}
	return p
}

// FilesFromManifest converts manifest entries to metainfo file infos, sorted by
// path for a deterministic layout. Paths are split on "/" (manifest paths are
// always forward-slash relative).
func FilesFromManifest(m *manifest.Manifest) []metainfo.FileInfo {
	out := make([]metainfo.FileInfo, 0, len(m.Files))
	for _, f := range m.Files {
		out = append(out, metainfo.FileInfo{
			Length: f.Size,
			Path:   strings.Split(f.Path, "/"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Join(out[i].Path, "/") < strings.Join(out[j].Path, "/")
	})
	return out
}

// BuildInfo assembles a metainfo.Info and streams the piece hashes by reading
// each file from datadir. pieceLen<=0 → AutoPieceLength(total). Returns the
// populated Info (Pieces filled) ready to bencode.
func BuildInfo(datadir, name string, files []metainfo.FileInfo, pieceLen int64) (*metainfo.Info, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("torrentbuild: no files to include")
	}
	var total int64
	for _, f := range files {
		total += f.Length
	}
	if pieceLen <= 0 {
		pieceLen = AutoPieceLength(total)
	}
	info := &metainfo.Info{
		Name:        name,
		PieceLength: pieceLen,
		Files:       files,
	}
	open := func(fi metainfo.FileInfo) (io.ReadCloser, error) {
		p := filepath.Join(append([]string{datadir}, fi.Path...)...)
		return os.Open(p)
	}
	if err := info.GeneratePieces(open); err != nil {
		return nil, fmt.Errorf("torrentbuild: generate pieces: %w", err)
	}
	return info, nil
}

// MetaOpts are the out-of-info-dict fields (do NOT affect the infohash).
type MetaOpts struct {
	Trackers     []string // announce-list (BEP12)
	WebSeeds     []string // url-list (BEP19) — HTTP mirrors for hybrid seeding
	Comment      string
	CreatedBy    string
	CreationDate int64 // fixed for reproducibility (e.g. release height or 0)
}

// BuildMetaInfo bencodes the info dict into a MetaInfo, attaches the announce/
// webseed metadata, and returns it with its infohash. The infohash is computed
// from InfoBytes only, so it is identical for any MetaOpts.
func BuildMetaInfo(info *metainfo.Info, opts MetaOpts) (*metainfo.MetaInfo, metainfo.Hash, error) {
	infoBytes, err := bencode.Marshal(*info)
	if err != nil {
		return nil, metainfo.Hash{}, fmt.Errorf("torrentbuild: marshal info: %w", err)
	}
	mi := &metainfo.MetaInfo{
		InfoBytes:    infoBytes,
		Comment:      opts.Comment,
		CreatedBy:    opts.CreatedBy,
		CreationDate: opts.CreationDate,
	}
	if len(opts.Trackers) > 0 {
		mi.Announce = opts.Trackers[0]
		al := make(metainfo.AnnounceList, 0, len(opts.Trackers))
		for _, t := range opts.Trackers {
			al = append(al, []string{t})
		}
		mi.AnnounceList = al
	}
	if len(opts.WebSeeds) > 0 {
		mi.UrlList = append(metainfo.UrlList{}, opts.WebSeeds...)
	}
	return mi, mi.HashInfoBytes(), nil
}

// Magnet returns the magnet URI for a built torrent (infohash + name + trackers).
func Magnet(mi *metainfo.MetaInfo, info *metainfo.Info) string {
	ih := mi.HashInfoBytes()
	return mi.Magnet(&ih, info).String()
}
