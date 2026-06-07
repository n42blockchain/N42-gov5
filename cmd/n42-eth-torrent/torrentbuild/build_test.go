// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package torrentbuild

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/cmd/n42-eth-manifest/manifest"
)

func TestAutoPieceLength_Bounds(t *testing.T) {
	if got := AutoPieceLength(0); got != minPieceLen {
		t.Errorf("zero total → %d, want minPieceLen %d", got, minPieceLen)
	}
	// 1 KiB total → still clamped to the floor.
	if got := AutoPieceLength(1024); got != minPieceLen {
		t.Errorf("tiny total → %d, want %d", got, minPieceLen)
	}
	// 10 TiB total → should clamp to the 16 MiB ceiling.
	if got := AutoPieceLength(10 << 40); got != maxPieceLen {
		t.Errorf("huge total → %d, want maxPieceLen %d", got, maxPieceLen)
	}
	// power-of-two always.
	for _, total := range []int64{1 << 30, 137 << 30, 800 << 30} {
		p := AutoPieceLength(total)
		if p&(p-1) != 0 {
			t.Errorf("piece length %d for total %d is not a power of two", p, total)
		}
		if p < minPieceLen || p > maxPieceLen {
			t.Errorf("piece length %d out of bounds", p)
		}
	}
}

// buildTree writes name→content files under a temp datadir and returns
// (datadir, manifest).
func buildTree(t *testing.T, files map[string][]byte) (string, *manifest.Manifest) {
	t.Helper()
	root := t.TempDir()
	man := &manifest.Manifest{Network: "mainnet", Mode: "minimal", Height: 100}
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
		man.Files = append(man.Files, &manifest.FileEntry{Path: name, Size: int64(len(content))})
	}
	return root, man
}

func TestBuildInfo_ReproducibleInfohash(t *testing.T) {
	// Two files spanning multiple small pieces.
	root, man := buildTree(t, map[string][]byte{
		"snapshot/a.val.zst": bytes.Repeat([]byte{0xAB}, 5000),
		"snapshot/b.ef":      bytes.Repeat([]byte{0xCD}, 3000),
	})
	files := FilesFromManifest(man)

	info1, err := BuildInfo(root, "n42-test", files, 1024)
	if err != nil {
		t.Fatal(err)
	}
	info2, err := BuildInfo(root, "n42-test", files, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(info1.Pieces, info2.Pieces) {
		t.Fatal("piece hashes are not reproducible across builds")
	}
	// 5000+3000 = 8000 bytes over 1024-byte pieces = ceil(8000/1024) = 8 pieces.
	if info1.NumPieces() != 8 {
		t.Errorf("NumPieces = %d, want 8", info1.NumPieces())
	}
	if len(info1.Pieces) != 8*20 {
		t.Errorf("Pieces len = %d, want %d (20 bytes/piece)", len(info1.Pieces), 8*20)
	}

	// Infohash must be identical AND independent of trackers/webseeds (those live
	// outside the info dict).
	_, ih1, err := BuildMetaInfo(info1, MetaOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_, ih2, err := BuildMetaInfo(info2, MetaOpts{
		Trackers: []string{"udp://tracker:6969"},
		WebSeeds: []string{"https://mirror/"},
		Comment:  "different comment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ih1 != ih2 {
		t.Errorf("infohash changed with announce metadata: %s != %s", ih1.HexString(), ih2.HexString())
	}
}

func TestBuildMetaInfo_AttachesAnnounceAndWebseed(t *testing.T) {
	root, man := buildTree(t, map[string][]byte{
		"snapshot/x.val.zst": bytes.Repeat([]byte{1}, 2048),
	})
	info, err := BuildInfo(root, "n42-test", FilesFromManifest(man), 1024)
	if err != nil {
		t.Fatal(err)
	}
	mi, _, err := BuildMetaInfo(info, MetaOpts{
		Trackers: []string{"udp://t1:6969", "udp://t2:6969"},
		WebSeeds: []string{"https://mirror/mainnet/"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if mi.Announce != "udp://t1:6969" {
		t.Errorf("Announce = %q, want first tracker", mi.Announce)
	}
	if len(mi.AnnounceList) != 2 {
		t.Errorf("AnnounceList len = %d, want 2", len(mi.AnnounceList))
	}
	if len(mi.UrlList) != 1 || mi.UrlList[0] != "https://mirror/mainnet/" {
		t.Errorf("UrlList = %v, want one webseed", mi.UrlList)
	}
	magnet := Magnet(mi, info)
	if !bytes.HasPrefix([]byte(magnet), []byte("magnet:?xt=urn:btih:")) {
		t.Errorf("magnet missing btih prefix: %s", magnet)
	}
}

func TestBuildInfo_EmptyErrors(t *testing.T) {
	if _, err := BuildInfo(t.TempDir(), "x", nil, 1024); err == nil {
		t.Error("expected error for empty file set")
	}
}

func TestBuildInfo_MissingFileErrors(t *testing.T) {
	root := t.TempDir()
	man := &manifest.Manifest{Files: []*manifest.FileEntry{{Path: "snapshot/nope.zst", Size: 10}}}
	if _, err := BuildInfo(root, "x", FilesFromManifest(man), 1024); err == nil {
		t.Error("expected error when a manifest file is absent on disk")
	}
}
