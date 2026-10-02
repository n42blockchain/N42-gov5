package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"
)

// ethexecBundleFlags builds the flag set shared by bundle-hash and
// bundle-verify, letting the test drive runBundleHash/runBundleVerify exactly
// as the CLI would, against a small temp-dir fixture instead of a real
// datadir.
func ethexecBundleContext(t *testing.T, flags []cli.Flag, values map[string]string, bools map[string]bool) *cli.Context {
	t.Helper()
	app := cli.NewApp()
	app.Flags = flags
	set := flag.NewFlagSet("test", 0)
	for _, f := range flags {
		for _, name := range f.Names() {
			switch f.(type) {
			case *cli.BoolFlag:
				set.Bool(name, false, "")
			case *cli.Uint64Flag:
				set.Uint64(name, 0, "")
			case *cli.IntFlag:
				set.Int(name, 0, "")
			default:
				set.String(name, "", "")
			}
		}
	}
	ctx := cli.NewContext(app, set, nil)
	for k, v := range values {
		if err := set.Set(k, v); err != nil {
			t.Fatalf("set %s=%s: %v", k, v, err)
		}
	}
	for k, v := range bools {
		if v {
			if err := set.Set(k, "true"); err != nil {
				t.Fatalf("set %s: %v", k, err)
			}
		}
	}
	return ctx
}

func TestEthexecBundleHashAndVerifyRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.dat"), []byte("hello world"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.dat"), []byte("another file"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")

	hashFlags := []cli.Flag{
		&cli.StringFlag{Name: "datadir"},
		&cli.StringFlag{Name: "out"},
		&cli.Uint64Flag{Name: "chain-id"},
		&cli.Uint64Flag{Name: "block-start"},
		&cli.Uint64Flag{Name: "block-end"},
		&cli.IntFlag{Name: "workers"},
		&cli.StringFlag{Name: "algo"},
		&cli.BoolFlag{Name: "include-all"},
	}
	hashCtx := ethexecBundleContext(t, hashFlags,
		map[string]string{"datadir": root, "out": manifestPath, "chain-id": "94"},
		map[string]bool{"include-all": true},
	)
	if err := runBundleHash(hashCtx); err != nil {
		t.Fatalf("runBundleHash error: %v", err)
	}
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("expected manifest file to be written: %v", err)
	}

	verifyFlags := []cli.Flag{
		&cli.StringFlag{Name: "datadir"},
		&cli.StringFlag{Name: "manifest"},
		&cli.IntFlag{Name: "workers"},
	}
	verifyCtx := ethexecBundleContext(t, verifyFlags,
		map[string]string{"datadir": root, "manifest": manifestPath}, nil)
	if err := runBundleVerify(verifyCtx); err != nil {
		t.Fatalf("runBundleVerify error: %v", err)
	}
}

func TestEthexecBundleVerifyDetectsCorruption(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.dat"), []byte("hello world"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")

	hashFlags := []cli.Flag{
		&cli.StringFlag{Name: "datadir"},
		&cli.StringFlag{Name: "out"},
		&cli.Uint64Flag{Name: "chain-id"},
		&cli.Uint64Flag{Name: "block-start"},
		&cli.Uint64Flag{Name: "block-end"},
		&cli.IntFlag{Name: "workers"},
		&cli.StringFlag{Name: "algo"},
		&cli.BoolFlag{Name: "include-all"},
	}
	hashCtx := ethexecBundleContext(t, hashFlags,
		map[string]string{"datadir": root, "out": manifestPath},
		map[string]bool{"include-all": true},
	)
	if err := runBundleHash(hashCtx); err != nil {
		t.Fatalf("runBundleHash error: %v", err)
	}

	// Corrupt the file after hashing so verify must detect the mismatch.
	if err := os.WriteFile(filepath.Join(root, "a.dat"), []byte("tampered!!!!"), 0o600); err != nil {
		t.Fatalf("corrupt fixture: %v", err)
	}

	verifyFlags := []cli.Flag{
		&cli.StringFlag{Name: "datadir"},
		&cli.StringFlag{Name: "manifest"},
		&cli.IntFlag{Name: "workers"},
	}
	verifyCtx := ethexecBundleContext(t, verifyFlags,
		map[string]string{"datadir": root, "manifest": manifestPath}, nil)
	if err := runBundleVerify(verifyCtx); err == nil {
		t.Fatal("expected runBundleVerify to fail on corrupted data")
	}
}
