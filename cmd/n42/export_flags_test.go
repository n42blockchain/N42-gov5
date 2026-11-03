package main

import (
	"github.com/urfave/cli/v2"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestN42ValidateDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := validateDirectory(dir, "datadir"); err != nil {
		t.Fatalf("validateDirectory on a real dir: %v", err)
	}

	missing := filepath.Join(dir, "does-not-exist")
	if err := validateDirectory(missing, "datadir"); err == nil {
		t.Fatal("expected error for a missing path")
	}

	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := validateDirectory(file, "datadir"); err == nil {
		t.Fatal("expected error when path is a file, not a directory")
	}
}

func TestN42AllFlagsNonEmpty(t *testing.T) {
	flags := AllFlags()
	if len(flags) == 0 {
		t.Fatal("AllFlags returned no flags")
	}
	seen := map[string]bool{}
	for _, f := range flags {
		for _, name := range f.Names() {
			if seen[name] {
				t.Errorf("duplicate CLI flag %q", name)
			}
			seen[name] = true
		}
	}
	for _, want := range []string{"datadir", "debug"} {
		if !seen[want] {
			t.Errorf("AllFlags missing expected flag %q", want)
		}
	}
}

func TestIngestFlagAliasesParse(t *testing.T) {
	saved := DefaultConfig.IngestCfg
	defer func() { DefaultConfig.IngestCfg = saved }()
	for _, flag := range []string{"--ingest", "--ingest.enabled"} {
		app := &cli.App{Flags: AllFlags(), Writer: io.Discard, ErrWriter: io.Discard,
			Action: func(ctx *cli.Context) error { return nil }}
		if err := app.Run([]string{"n42", flag, "--ingest.addr", "127.0.0.1:19100", "--ingest.hardcap", "1234"}); err != nil {
			t.Fatal(err)
		}
		if !DefaultConfig.IngestCfg.Enabled || DefaultConfig.IngestCfg.Addr != "127.0.0.1:19100" || DefaultConfig.IngestCfg.HardCap != 1234 {
			t.Fatalf("ingest configuration not applied: %+v", DefaultConfig.IngestCfg)
		}
	}
}
