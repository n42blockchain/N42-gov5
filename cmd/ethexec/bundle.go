// Copyright 2022-2026 The N42 Authors
//
// bundle.go — CLI handlers for the minimal-client bundle hash/verify
// workflow. See internal/bundle for the manifest format and hashing
// logic; this file is the urfave/cli wiring + human-readable progress
// reporting.

package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/internal/bundle"
	"github.com/n42blockchain/N42/log"
)

// permissiveMatcher accepts any regular file under root that is NOT a
// transient operator artifact (locks, staging temps, the manifest
// being written, hidden dotfiles). Used by --include-all for archive
// dirs that don't follow the chain/freezer layout (snapshot, history).
func permissiveMatcher(relPath string, info os.FileInfo) bool {
	if info.IsDir() {
		return false
	}
	name := strings.ToLower(info.Name())
	switch {
	case name == "manifest.json":
		return false // don't hash the output into itself
	case strings.HasPrefix(name, "."):
		return false
	case strings.HasSuffix(name, ".lock"), strings.HasSuffix(name, ".lck"):
		return false
	case strings.HasSuffix(name, ".tmp"), strings.HasSuffix(name, ".staging"):
		return false
	case strings.HasSuffix(name, ".new"), strings.HasSuffix(name, ".bak"):
		return false
	}
	return true
}

func runBundleHash(c *cli.Context) error {
	root := c.String("datadir")
	outPath := c.String("out")
	opts := bundle.BuildOptions{
		ChainID:    c.Uint64("chain-id"),
		BlockRange: bundle.BlockRange{Start: c.Uint64("block-start"), End: c.Uint64("block-end")},
		Workers:    c.Int("workers"),
		Algorithm:  c.String("algo"),
		Progress: func(files, totalFiles, bytes, totalBytes int64) {
			pct := 0.0
			if totalBytes > 0 {
				pct = 100 * float64(bytes) / float64(totalBytes)
			}
			log.Info("bundle-hash progress",
				"files", fmt.Sprintf("%d/%d", files, totalFiles),
				"GB", fmt.Sprintf("%.1f/%.1f", float64(bytes)/1e9, float64(totalBytes)/1e9),
				"pct", fmt.Sprintf("%.1f%%", pct))
		},
	}

	if c.Bool("include-all") {
		opts.Matcher = permissiveMatcher
	}

	t0 := time.Now()
	log.Info("bundle-hash starting", "root", root, "out", outPath,
		"chainID", opts.ChainID, "blocks", fmt.Sprintf("%d..%d", opts.BlockRange.Start, opts.BlockRange.End))

	m, err := bundle.Build(root, opts)
	if err != nil {
		return fmt.Errorf("build manifest: %w", err)
	}
	if err := m.Save(outPath); err != nil {
		return err
	}

	var totalBytes int64
	for _, f := range m.Files {
		totalBytes += f.Size
	}
	log.Info("bundle-hash complete",
		"files", len(m.Files),
		"GB", fmt.Sprintf("%.2f", float64(totalBytes)/1e9),
		"elapsed", time.Since(t0).Truncate(time.Second),
		"manifest", outPath)
	return nil
}

func runBundleVerify(c *cli.Context) error {
	root := c.String("datadir")
	manifestPath := c.String("manifest")

	m, err := bundle.Load(manifestPath)
	if err != nil {
		return err
	}
	log.Info("bundle-verify starting",
		"root", root,
		"manifest", manifestPath,
		"files", len(m.Files),
		"chainID", m.ChainID,
		"blocks", fmt.Sprintf("%d..%d", m.BlockRange.Start, m.BlockRange.End),
		"generated_at", m.GeneratedAt.Format(time.RFC3339))

	opts := bundle.VerifyOptions{
		Workers: c.Int("workers"),
		Progress: func(files, totalFiles, bytes, totalBytes int64) {
			pct := 0.0
			if totalBytes > 0 {
				pct = 100 * float64(bytes) / float64(totalBytes)
			}
			log.Info("bundle-verify progress",
				"files", fmt.Sprintf("%d/%d", files, totalFiles),
				"GB", fmt.Sprintf("%.1f/%.1f", float64(bytes)/1e9, float64(totalBytes)/1e9),
				"pct", fmt.Sprintf("%.1f%%", pct))
		},
	}

	t0 := time.Now()
	results, err := bundle.Verify(root, m, opts)
	if err != nil {
		return err
	}

	// Tally results. Exit code is non-zero if any file failed, so
	// operators can chain with shell scripts (&& continue, || retry).
	var okCount, badCount int
	for _, r := range results {
		if r.Status == bundle.StatusOK {
			okCount++
			continue
		}
		badCount++
		switch r.Status {
		case bundle.StatusPartialMismatch:
			log.Error("bundle-verify: partial mismatch (retry these segments)",
				"file", r.Path, "bad_segments", r.BadSegments)
		default:
			log.Error("bundle-verify: file failed",
				"file", r.Path, "status", r.Status.String(), "err", r.Err)
		}
	}

	log.Info("bundle-verify complete",
		"ok", okCount,
		"bad", badCount,
		"elapsed", time.Since(t0).Truncate(time.Second))
	if badCount > 0 {
		return fmt.Errorf("bundle-verify: %d file(s) failed", badCount)
	}
	return nil
}
