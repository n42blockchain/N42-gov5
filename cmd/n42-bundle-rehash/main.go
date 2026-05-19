// n42-bundle-rehash: regenerate a freezer manifest with the latest
// default hash algorithm (BLAKE3 as of 2026-05). Reads the existing
// manifest.json, walks the same rootDir, recomputes every file's
// digest with BLAKE3, sanity-checks the file set and sizes haven't
// changed, then atomically replaces the manifest.
//
// Use case: operators that built manifests pre-2026-05 with BLAKE2b
// run this once after upgrading n42. New manifests verify ~2-3×
// faster on AMD64 thanks to BLAKE3's intrinsic Merkle parallelism;
// downstream clients with newer n42 binaries handle BLAKE3 manifests
// natively (legacy BLAKE2b is still accepted by Verify for any
// bundles that aren't migrated yet).
//
// Flow:
//
//	1. Load existing manifest (any supported algorithm).
//	2. If already at the target algorithm and --force not set: exit 0.
//	3. Rebuild from rootDir with the target algorithm (default BLAKE3).
//	4. Sanity check: file set + per-file Size are identical to the
//	   pre-migration manifest. Any drift aborts before overwriting.
//	5. Atomically replace: write .new, fsync, rename.
//	6. Verify the new manifest hashes match by running bundle.Verify.
//
// Steps 4 and 6 are belt-and-suspenders: step 4 catches "the freezer
// directory changed under us" (operator forgot to stop writers);
// step 6 catches "hash function bug" (newHasher returns the wrong
// digest length for some algorithm).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/internal/bundle"
	"github.com/n42blockchain/N42/log"
)

func main() {
	root := flag.String("root", "", "freezer/bundle root dir (contains chain/freezer/...)")
	manifestPath := flag.String("manifest", "", "existing manifest path (default: <root>/manifest.json)")
	outPath := flag.String("out", "", "output path (default: overwrite --manifest)")
	algo := flag.String("algo", bundle.DefaultAlgorithm, "target hash algorithm (default: blake3-256)")
	force := flag.Bool("force", false, "rehash even if manifest is already at --algo")
	dryRun := flag.Bool("dry-run", false, "compute new manifest in memory, verify, but DON'T overwrite")
	workers := flag.Int("workers", 0, "parallel hash workers (0 = GOMAXPROCS)")
	flag.Parse()

	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-bundle-rehash --root <dir> [--manifest path] [--algo blake3-256] [--dry-run]")
		os.Exit(1)
	}
	if *manifestPath == "" {
		*manifestPath = filepath.Join(*root, "manifest.json")
	}
	if *outPath == "" {
		*outPath = *manifestPath
	}

	old, err := bundle.Load(*manifestPath)
	must(err, "load existing manifest")
	log.Info("loaded existing manifest",
		"path", *manifestPath,
		"algorithm", old.Algorithm,
		"files", len(old.Files),
		"generated_at", old.GeneratedAt.Format(time.RFC3339))

	if old.Algorithm == *algo && !*force {
		log.Info("manifest already at target algorithm; nothing to do (use --force to rehash anyway)",
			"algorithm", *algo)
		return
	}

	t0 := time.Now()
	log.Info("rebuilding manifest", "root", *root, "target_algorithm", *algo)
	fresh, err := bundle.Build(*root, bundle.BuildOptions{
		ChainID:    old.ChainID,
		BlockRange: old.BlockRange,
		Algorithm:  *algo,
		Workers:    *workers,
		Progress: func(files, totalFiles, bytes, totalBytes int64) {
			pct := 0.0
			if totalBytes > 0 {
				pct = 100 * float64(bytes) / float64(totalBytes)
			}
			log.Info("rehash progress",
				"files", fmt.Sprintf("%d/%d", files, totalFiles),
				"GB", fmt.Sprintf("%.1f/%.1f", float64(bytes)/1e9, float64(totalBytes)/1e9),
				"pct", fmt.Sprintf("%.1f%%", pct))
		},
	})
	must(err, "build new manifest")

	if err := sanityCheck(old, fresh); err != nil {
		fatal("sanity check failed (refusing to overwrite): %v", err)
	}

	verifyResults, err := bundle.Verify(*root, fresh, bundle.VerifyOptions{Workers: *workers})
	must(err, "verify fresh manifest")
	for _, r := range verifyResults {
		if r.Status != bundle.StatusOK {
			fatal("fresh manifest failed self-verify on %s: status=%v err=%v",
				r.Path, r.Status, r.Err)
		}
	}

	elapsed := time.Since(t0).Truncate(time.Second)
	var totalBytes int64
	for _, f := range fresh.Files {
		totalBytes += f.Size
	}
	log.Info("rehash complete",
		"files", len(fresh.Files),
		"GB", fmt.Sprintf("%.2f", float64(totalBytes)/1e9),
		"elapsed", elapsed,
		"throughput_GBs", fmt.Sprintf("%.2f", float64(totalBytes)/1e9/elapsed.Seconds()))

	if *dryRun {
		log.Info("--dry-run set, NOT overwriting manifest",
			"would_write", *outPath, "old_algorithm", old.Algorithm, "new_algorithm", fresh.Algorithm)
		return
	}

	if err := saveAtomic(fresh, *outPath); err != nil {
		fatal("save: %v", err)
	}
	log.Info("manifest replaced atomically",
		"path", *outPath,
		"old_algorithm", old.Algorithm,
		"new_algorithm", fresh.Algorithm)
}

// sanityCheck ensures the freezer hasn't drifted between when the old
// manifest was generated and the rehash run. We assume operator stopped
// writers, but a bug in the operator's runbook would silently shift
// hashes; catch that here.
func sanityCheck(old, fresh *bundle.Manifest) error {
	if old.ChainID != fresh.ChainID {
		return fmt.Errorf("chainID changed: old=%d new=%d", old.ChainID, fresh.ChainID)
	}
	if old.BlockRange != fresh.BlockRange {
		return fmt.Errorf("block range changed: old=%v new=%v", old.BlockRange, fresh.BlockRange)
	}
	if len(old.Files) != len(fresh.Files) {
		return fmt.Errorf("file count changed: old=%d new=%d", len(old.Files), len(fresh.Files))
	}
	oldByPath := make(map[string]bundle.File, len(old.Files))
	for _, f := range old.Files {
		oldByPath[f.Path] = f
	}
	for _, f := range fresh.Files {
		o, ok := oldByPath[f.Path]
		if !ok {
			return fmt.Errorf("new file appeared: %s", f.Path)
		}
		if o.Size != f.Size {
			return fmt.Errorf("size changed for %s: old=%d new=%d", f.Path, o.Size, f.Size)
		}
	}
	return nil
}

// saveAtomic writes the manifest to <path>.new, fsyncs, then renames
// over <path>. On Linux this is filesystem-atomic; on Windows the
// rename is best-effort but happens after fsync so on-disk integrity
// is preserved regardless of crash timing.
func saveAtomic(m *bundle.Manifest, path string) error {
	tmp := path + ".new"
	if err := m.Save(tmp); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	// Best-effort fsync — Save closes the file so the kernel buffer
	// may already be flushed, but call fsync via reopen to force
	// directory entry durability before the rename.
	if f, err := os.OpenFile(tmp, os.O_RDWR, 0); err == nil {
		_ = f.Sync()
		f.Close()
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename tmp → final: %w", err)
	}
	return nil
}

func must(err error, what string) {
	if err != nil {
		fatal("%s: %v", what, err)
	}
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
