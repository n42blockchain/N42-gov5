package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestN42MakePasswordListNoFlag(t *testing.T) {
	set := flag.NewFlagSet("test", 0)
	ctx := cli.NewContext(nil, set, nil)
	if got := MakePasswordList(ctx); got != nil {
		t.Fatalf("MakePasswordList with no flag = %v, want nil", got)
	}
}

func TestN42MakePasswordListFromFile(t *testing.T) {
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pwFile, []byte("hunter2\r\nsecond-line\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}

	app := cli.NewApp()
	app.Flags = []cli.Flag{PasswordFileFlag}
	set := flag.NewFlagSet("test", 0)
	set.String(PasswordFileFlag.Name, "", "")
	ctx := cli.NewContext(app, set, nil)
	if err := set.Set(PasswordFileFlag.Name, pwFile); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	got := MakePasswordList(ctx)
	if len(got) != 3 || got[0] != "hunter2" || got[1] != "second-line" {
		t.Fatalf("MakePasswordList = %#v, want [hunter2 second-line \"\"]", got)
	}
}

func TestN42MakePasswordListMissingFile(t *testing.T) {
	app := cli.NewApp()
	app.Flags = []cli.Flag{PasswordFileFlag}
	set := flag.NewFlagSet("test", 0)
	set.String(PasswordFileFlag.Name, "", "")
	ctx := cli.NewContext(app, set, nil)
	if err := set.Set(PasswordFileFlag.Name, "/nonexistent/path/pw.txt"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	if got := MakePasswordList(ctx); got != nil {
		t.Fatalf("MakePasswordList with missing file = %v, want nil", got)
	}
}

func TestN42AccountCreate(t *testing.T) {
	originalDataDir := DefaultConfig.NodeCfg.DataDir
	originalKeyStoreDir := DefaultConfig.NodeCfg.KeyStoreDir
	originalLightKDF := DefaultConfig.NodeCfg.UseLightweightKDF
	t.Cleanup(func() {
		DefaultConfig.NodeCfg.DataDir = originalDataDir
		DefaultConfig.NodeCfg.KeyStoreDir = originalKeyStoreDir
		DefaultConfig.NodeCfg.UseLightweightKDF = originalLightKDF
	})

	DefaultConfig.NodeCfg.DataDir = t.TempDir()
	DefaultConfig.NodeCfg.KeyStoreDir = ""
	DefaultConfig.NodeCfg.UseLightweightKDF = true // avoid the standard scrypt cost in a test

	app := cli.NewApp()
	app.Flags = []cli.Flag{PasswordFileFlag}
	set := flag.NewFlagSet("test", 0)
	set.String(PasswordFileFlag.Name, "", "")
	ctx := cli.NewContext(app, set, nil)
	// Supply a password via -- no password file set, so GetPassPhraseWithList
	// falls through to MakePasswordList returning nil and then prompts --
	// instead, point the password file at a temp file so it never touches stdin.
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pwFile, []byte("test-password\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	if err := set.Set(PasswordFileFlag.Name, pwFile); err != nil {
		t.Fatalf("set flag: %v", err)
	}

	if err := accountCreate(ctx); err != nil {
		t.Fatalf("accountCreate error: %v", err)
	}

	keydir, err := DefaultConfig.NodeCfg.KeyDirConfig()
	if err != nil {
		t.Fatalf("KeyDirConfig error: %v", err)
	}
	entries, err := os.ReadDir(keydir)
	if err != nil {
		t.Fatalf("read keydir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected accountCreate to write a keystore file")
	}
}
