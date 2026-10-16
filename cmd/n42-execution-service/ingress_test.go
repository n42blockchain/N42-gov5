package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/params"
)

func ingressManifestForTest(t *testing.T) []byte {
	t.Helper()
	d := sharedexec.CorpusDescriptor{Version: sharedexec.CorpusVersion, Identity: sharedexec.Identity{Version: 1, Namespace: params.PerformanceNamespace, ChainID: "4205001", GenesisHash: types.Hash{1}}, GenesisFileHash: types.Hash{2}, Codec: sharedexec.CorpusCodec, Signature: sharedexec.CorpusSignature, KeyDerivation: sharedexec.CorpusKeyDerivation, SeedCommitment: types.Hash{3}, SenderCount: 1, NoncesPerSender: 1, RecipientRule: sharedexec.CorpusRecipientRule, RecipientCount: 1, ShardCount: 1, FrameTransactions: 1}
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(sharedexec.CorpusManifest{Descriptor: d, CorpusID: id, Shards: []sharedexec.CorpusShard{{Index: 0, File: "shard-00.gcf", SHA256: types.Hash{4}, Bytes: 89, Frames: 1, Transactions: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func TestStrictIngressOptionsRequireBoundedExplicitManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	raw := ingressManifestForTest(t)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	opts, err := strictIngressOptions(path, 0, 0)
	if err != nil || opts == nil || opts.MaxTransactions != 2_000_000 || opts.MaxEncodedBytes != 512<<20 || !bytes.Equal(opts.Manifest, raw) {
		t.Fatalf("explicit bounded options %+v %v", opts, err)
	}
	if opts, err := strictIngressOptions("", 0, 0); err != nil || opts != nil {
		t.Fatal("default owner enabled strict ingress")
	}
	link := filepath.Join(dir, "alias.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	oversized := filepath.Join(dir, "oversized.json")
	if err := os.WriteFile(oversized, make([]byte, sharedexec.CorpusMaxManifestBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "blocked-fifo")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := strictIngressOptions(fifo, 1, 1); err == nil {
		t.Fatal("FIFO manifest accepted")
	}
	if time.Since(start) > time.Second {
		t.Fatal("FIFO manifest blocked startup")
	}
	invalid := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{} {}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path       string
		txs, bytes int
	}{{"", 1, 0}, {"", 0, 1}, {"relative.json", 1, 1}, {link, 1, 1}, {dir, 1, 1}, {oversized, 1, 1}, {invalid, 1, 1}, {path, -1, 1}, {path, 2_000_001, 1}, {path, 1, -1}, {path, 1, (512 << 20) + 1}} {
		if _, err := strictIngressOptions(tc.path, tc.txs, tc.bytes); err == nil {
			t.Fatalf("unsafe ingress options accepted %+v", tc)
		}
	}
}
func TestStrictIngressCLIRejectsBadOptionsBeforeCreatingData(t *testing.T) {
	target := filepath.Join(t.TempDir(), "not-created")
	base := []string{"--genesis", "/explicit/not-read.json", "--data-dir", target}
	for _, extra := range [][]string{{"--ingress-max-transactions", "0"}, {"--ingress-max-encoded-bytes", "1"}, {"--strict-ingress-manifest", "relative.json"}, {"--strict-ingress-manifest", "/missing.json", "--ingress-max-transactions", "2000001"}} {
		args := append(append([]string(nil), base...), extra...)
		if err := run(context.Background(), args); err == nil {
			t.Fatal("unsafe ingress CLI accepted")
		}
		if _, err := os.Lstat(target); !os.IsNotExist(err) {
			t.Fatal("invalid ingress flags created datadir")
		}
	}
}
