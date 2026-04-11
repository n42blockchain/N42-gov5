// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

// Package kvadapter opens an MDBX environment dedicated to Caplin (the
// embedded consensus layer). The environment is intentionally separate from
// the n42-el chaindata MDBX:
//
//   - it lives under <BeaconCfg.DataDir> (default <ethexec-datadir>/caplin),
//   - it uses its own page size, map size and growth step tuned for the
//     CL working set (much smaller than the EL chaindata),
//   - it registers only the bucket subset Caplin needs, so we never
//     accidentally pollute or read EL tables, and
//   - it never shares an mdbx_env handle with the EL — Windows in
//     particular handles two writable mmap environments more reliably than
//     a single shared one (see memory: MDBX Windows OOM).
//
// The package returns a `kv.RwDB` from N42's lib/kv. Inside the cl/ tree,
// Caplin code uses `internal/cl/depshim/kv.RwDB`, which is a type alias to
// the same lib/kv interface — so the value returned here can be passed
// straight in.
package kvadapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/c2h5oh/datasize"

	libkv "github.com/n42blockchain/N42/lib/kv"
	libmdbx "github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
)

// Config controls the Caplin MDBX instance.
type Config struct {
	// DataDir is the directory containing the MDBX environment. The directory
	// is created (with parents) if it does not exist.
	DataDir string

	// MapSize is the maximum mmap reservation. CL state for mainnet sits at
	// roughly 30 GiB total; the default leaves headroom without running into
	// the auto-grow path.
	MapSize datasize.ByteSize

	// GrowthStep is how much MDBX expands the file by when it runs out of
	// space. Smaller is friendlier on Windows commit charge.
	GrowthStep datasize.ByteSize

	// PageSize must be a power of two between 256 B and 64 KiB. 4 KiB matches
	// the OS page on every supported platform.
	PageSize uint64

	// ReadOnly opens the environment without write permissions. Useful for
	// diagnostic tooling that wants to inspect Caplin state without holding
	// the writer lock.
	ReadOnly bool
}

// DefaultConfig returns sane defaults for a writable Caplin MDBX instance.
// Callers must still set DataDir.
func DefaultConfig() Config {
	return Config{
		MapSize:    32 * datasize.GB,
		GrowthStep: 1 * datasize.GB,
		PageSize:   4096,
	}
}

// Open creates (if needed) and opens the Caplin MDBX environment described
// by cfg. The returned RwDB is owned by the caller and must be Close()d
// during shutdown.
func Open(ctx context.Context, cfg Config) (libkv.RwDB, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("kvadapter: DataDir must be set")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("kvadapter: mkdir %s: %w", cfg.DataDir, err)
	}

	// Resolve defaults for any zero-valued fields the caller left empty.
	def := DefaultConfig()
	if cfg.MapSize == 0 {
		cfg.MapSize = def.MapSize
	}
	if cfg.GrowthStep == 0 {
		cfg.GrowthStep = def.GrowthStep
	}
	if cfg.PageSize == 0 {
		cfg.PageSize = def.PageSize
	}

	opts := libmdbx.NewMDBX(log.New("module", "caplin-kv")).
		Path(filepath.Clean(cfg.DataDir)).
		Label(libkv.ChainDB).
		PageSize(cfg.PageSize).
		MapSize(cfg.MapSize).
		GrowthStep(cfg.GrowthStep).
		WithTableCfg(buildTableCfg)

	if cfg.ReadOnly {
		opts = opts.Readonly()
	}

	db, err := opts.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("kvadapter: open mdbx %s: %w", cfg.DataDir, err)
	}
	log.Info("Caplin MDBX opened",
		"path", cfg.DataDir,
		"mapSize", cfg.MapSize.String(),
		"readonly", cfg.ReadOnly,
	)
	return db, nil
}
