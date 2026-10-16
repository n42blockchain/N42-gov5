package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func supplyRuntimeSourceFixture(t *testing.T) (string, string, manifest) {
	t.Helper()
	base, template := provisionTestPaths(t)
	source := filepath.Join(base, "source")
	if err := provisionFixture(source, template, 4, 25000, base, true); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := readFixtureJSON(filepath.Join(source, "run.json"), &m); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(source, "load", "queue", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(source, "manifest.json"), raw); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		name := []string{"shard-00.gcf", "shard-01.gcf", "shard-02.gcf", "shard-03.gcf"}[i]
		raw, err := os.ReadFile(filepath.Join(source, "load", "queue", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := writePrivate(filepath.Join(source, name), raw); err != nil {
			t.Fatal(err)
		}
	}
	return base, source, m
}
func sourceFiles(t *testing.T, source string) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	if err := filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = raw
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestSupplyRuntimePreservesGenesisAndCopiesOnlyMatchingValidatorKeys(t *testing.T) {
	base, source, original := supplyRuntimeSourceFixture(t)
	before := sourceFiles(t, source)
	root := filepath.Join(base, "runtime")
	if err := provisionSupplyRuntime(root, source, 28000, 100, base, base); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := readFixtureJSON(filepath.Join(root, "run.json"), &m); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != 2 || m.LoadKeys != "" || m.Supply == nil || m.Supply.Root != source || m.Supply.BlockIntervalMS != 100 || m.Identity != original.Identity {
		t.Fatal("source runtime binding changed")
	}
	raw, err := os.ReadFile(m.Genesis)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, before[original.Genesis]) {
		t.Fatal("source genesis bytes were re-encoded")
	}
	digest := sha256.Sum256(before[filepath.Join(source, "manifest.json")])
	if m.Supply.ManifestSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatal("manifest digest changed")
	}
	encoded, err := os.ReadFile(filepath.Join(root, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("load_keys")) {
		t.Fatal("schema2 pretends to have sender keys")
	}
	for _, path := range []string{"sender.seed", "load", "e/keystore"} {
		if _, err := os.Lstat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatal("sender material or BLS copied to owner")
		}
	}
	entries, err := os.ReadDir(m.OwnerDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("owner received runtime keys or state")
	}
	for i, v := range m.Validators {
		old := original.Validators[i]
		name := "bls_" + strings.ToLower(v.Address) + ".key"
		copied, err := os.ReadFile(filepath.Join(v.DataDir, "keystore", name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(copied, before[filepath.Join(old.DataDir, "keystore", name)]) {
			t.Fatal("validator BLS identity changed")
		}
		p2p, err := os.ReadFile(filepath.Join(v.DataDir, "p2p.key"))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(p2p, before[filepath.Join(old.DataDir, "p2p.key")]) {
			t.Fatal("P2P identity reused")
		}
		var cfg conf.Config
		if err := conf.LoadConfigFromFile(v.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		if !cfg.NodeCfg.PerformanceOwnerQueue || cfg.NodeCfg.PerformanceQueueMaxTransactions != 163000 || cfg.Miner.BlockIntervalMs != 100 || cfg.NodeCfg.PerformanceGenesis != m.Genesis {
			t.Fatal("supply runtime config not effective")
		}
	}
	after := sourceFiles(t, source)
	if len(after) != len(before) {
		t.Fatal("source corpus files changed")
	}
	for path, raw := range before {
		if !bytes.Equal(raw, after[path]) {
			t.Fatal("source corpus modified")
		}
	}
	if err := provisionSupplyRuntime(root, source, 28000, 100, base, base); err == nil {
		t.Fatal("existing runtime overwritten")
	}
}
func TestSupplyRuntimeRejectsWrongGenesisOrBLSBeforeWriting(t *testing.T) {
	for _, kind := range []string{"genesis", "bls", "source-alias", "pacing"} {
		t.Run(kind, func(t *testing.T) {
			base, source, m := supplyRuntimeSourceFixture(t)
			root := filepath.Join(base, "runtime")
			interval := uint64(100)
			switch kind {
			case "genesis":
				f, err := os.OpenFile(m.Genesis, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("\n")
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "bls":
				a := m.Validators[0]
				b := m.Validators[1]
				raw, err := os.ReadFile(filepath.Join(a.DataDir, "keystore", "bls_"+strings.ToLower(a.Address)+".key"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(b.DataDir, "keystore", "bls_"+strings.ToLower(b.Address)+".key"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "source-alias":
				alias := filepath.Join(base, "alias")
				if err := os.Symlink(source, alias); err != nil {
					t.Fatal(err)
				}
				source = alias
			case "pacing":
				interval = 60001
			}
			if err := provisionSupplyRuntime(root, source, 28000, interval, base, base); err == nil {
				t.Fatal("invalid source accepted")
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatal("invalid source created runtime data")
			}
		})
	}
}
func TestSupplySourceFIFOIsRejectedWithoutBlocking(t *testing.T) {
	base, _ := provisionTestPaths(t)
	path := filepath.Join(base, "fifo")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := readPrivateCorpusSource(path, 256); err == nil {
		t.Fatal("FIFO accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("FIFO source blocked")
	}
}

func TestSupplyRuntimeRejectsNoncanonicalValidatorAddressBeforeWriting(t *testing.T) {
	base, source, m := supplyRuntimeSourceFixture(t)
	root := filepath.Join(base, "runtime")
	var g conf.Genesis
	if err := readFixtureJSON(m.Genesis, &g); err != nil {
		t.Fatal(err)
	}
	g.Config.HotStuff.Validators[0].Address = "0x00" + strings.TrimPrefix(g.Config.HotStuff.Validators[0].Address, "0x")
	raw, err := os.ReadFile(m.Genesis)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["config"], err = json.Marshal(g.Config)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.Genesis, raw, 0600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(source, "manifest.json")
	var corpus sharedexec.CorpusManifest
	if err := readFixtureJSON(manifestPath, &corpus); err != nil {
		t.Fatal(err)
	}
	corpus.Descriptor.GenesisFileHash = types.Hash(sha256.Sum256(raw))
	gb, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		t.Fatal(err)
	}
	corpus.Descriptor.Identity.GenesisHash = gb.Hash()
	corpus.CorpusID, err = corpus.Descriptor.ID()
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := provisionSupplyRuntime(root, source, 28000, 100, base, base); err == nil || !strings.Contains(err.Error(), "invalid duplicate supply validator") {
		t.Fatalf("noncanonical address escaped exact precheck: %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("invalid address created runtime")
	}
}
