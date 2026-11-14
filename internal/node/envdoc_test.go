// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package node_test

import (
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/envswitch"

	// Packages the node does not link but that read N42_* switches.
	_ "github.com/n42blockchain/N42/crypto"
	_ "github.com/n42blockchain/N42/internal/api/consensusrest"
	_ "github.com/n42blockchain/N42/internal/ethel"
	_ "github.com/n42blockchain/N42/internal/ethel/eldevp2p"
	_ "github.com/n42blockchain/N42/internal/ethel/snapshotreader"
	_ "github.com/n42blockchain/N42/internal/txlookup"
	_ "github.com/n42blockchain/N42/lib/kv/mdbx"
	_ "github.com/n42blockchain/N42/lib/qmdb"
	_ "github.com/n42blockchain/N42/lib/trie"
	_ "github.com/n42blockchain/N42/log"
)

var updateEnvDoc = flag.Bool("update", false, "rewrite docs/ENV_SWITCHES.md")

const envDocPath = "../../docs/ENV_SWITCHES.md"

// renderEnvSwitchDoc renders the table of every registered switch.
func renderEnvSwitchDoc() string {
	var b strings.Builder
	b.WriteString("# N42_* environment switches\n\n")
	b.WriteString("Generated from the `common/envswitch` registry; do not edit by hand.\n")
	b.WriteString("Regenerate with `go test -tags nosqlite,noboltdb,n42el ./internal/node/ -run TestEnvSwitchDoc -update`.\n\n")
	b.WriteString("Scope: `perf` changes speed or memory only; `diag` adds logging or dumps; `config` selects a mode, path or size; ")
	b.WriteString("`consensus` changes a rule every validator must apply alike; `bench-only` must not be set on a production node.\n\n")
	b.WriteString("| Name | Scope | Kind | Default | Package | Description |\n")
	b.WriteString("|------|-------|------|---------|---------|-------------|\n")
	esc := func(s string) string { return strings.ReplaceAll(s, "|", "\\|") }
	for _, i := range envswitch.All() {
		pkgs := map[string]bool{}
		for _, s := range i.Sites {
			pkgs[path.Dir(s[:strings.LastIndex(s, ":")])] = true
		}
		var pl []string
		for p := range pkgs {
			pl = append(pl, p)
		}
		sort.Strings(pl)
		def := i.Default
		if def == "" {
			def = "-"
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n", i.Name, i.Scope, i.Kind, esc(def), esc(strings.Join(pl, ", ")), esc(i.Doc))
	}
	return b.String()
}

// TestEnvSwitchDoc fails when docs/ENV_SWITCHES.md is stale; -update rewrites it.
func TestEnvSwitchDoc(t *testing.T) {
	envswitch.CheckConflicts()
	want := renderEnvSwitchDoc()
	p := filepath.FromSlash(envDocPath)
	if *updateEnvDoc {
		if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update)", p, err)
	}
	if string(got) != want {
		t.Fatalf("%s is stale; run: go test -tags nosqlite,noboltdb,n42el ./internal/node/ -run TestEnvSwitchDoc -update", p)
	}
}
