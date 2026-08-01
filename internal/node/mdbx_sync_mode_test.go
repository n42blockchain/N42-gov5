// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The property under test is the default: a node must open its chaindata with
// the per-commit fsync ON unless someone explicitly asked otherwise. The
// override weakens a durability guarantee, so an accidental change of default
// — a typo in the env name, a switch that falls through, a refactor that drops
// the guard — has to fail here rather than in a power cut.

package node

import (
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"

	kvmdbx "github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func TestMdbxSyncModeDefaultsToDurable(t *testing.T) {
	logger := log2.New()
	for _, tc := range []struct {
		name    string
		env     string
		noSync  bool
		comment string
	}{
		{name: "unset", env: "", noSync: false},
		{name: "explicit durable", env: "durable", noSync: false},
		{name: "mixed case durable", env: "Durable", noSync: false},
		{name: "safe-nosync", env: "safe-nosync", noSync: true},
		{name: "safe-nosync padded", env: "  SAFE-NOSYNC  ", noSync: true},
		{name: "typo stays durable", env: "safenosync", noSync: false},
		{name: "unknown stays durable", env: "yes", noSync: false},
		{name: "true is not a mode", env: "true", noSync: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("N42_MDBX_SYNC", tc.env)
			opts := mdbxSyncModeOr(kvmdbx.NewMDBX(logger), logger)
			if got := opts.HasFlag(mdbx.SafeNoSync); got != tc.noSync {
				t.Fatalf("N42_MDBX_SYNC=%q: SafeNoSync=%v, want %v", tc.env, got, tc.noSync)
			}
		})
	}
}
