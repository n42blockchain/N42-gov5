package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func TestQueueFixtureBindsRealSignedUniqueNoncesAndSeparateConfigs(t *testing.T) {
	base, template := provisionTestPaths(t)
	root := filepath.Join(base, "q")
	if err := provisionFixture(root, template, 4, 25000, base, true); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := readFixtureJSON(filepath.Join(root, "run.json"), &m); err != nil {
		t.Fatal(err)
	}
	corpus, digest, frames, err := readQueueCorpus(m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "load", "queue", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if digest != types.Hash(sha256.Sum256(raw)) || len(frames) != 4 {
		t.Fatal("manifest/frame binding lost")
	}
	var g conf.Genesis
	if err := readFixtureJSON(m.Genesis, &g); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(filepath.Join(root, "load", "queue-seed"))
	if err != nil {
		t.Fatal(err)
	}
	if corpus.Descriptor.SeedCommitment != types.Hash(sha256.Sum256(seed)) {
		t.Fatal("seed commitment differs")
	}
	hashes := map[types.Hash]bool{}
	for i, frame := range frames {
		key, err := queueFixtureKey(seed, uint32(i))
		if err != nil {
			t.Fatal(err)
		}
		nonce := uint64(0)
		err = sharedexec.WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
			tx, err := transaction.DecodeEthereumTransaction(raw)
			if err != nil {
				return err
			}
			sender, err := transaction.NewEIP155Signer(g.Config.ChainID).Sender(tx)
			if err != nil {
				return err
			}
			expectedTo := queueRecipient(uint32((uint64(i)*2 + nonce) % 4))
			if tx.To() == nil || *tx.To() != expectedTo {
				t.Fatal("recipient derivation differs from corpus rule")
			}
			if sender != crypto.PubkeyToAddress(key.PublicKey) || tx.Nonce() != nonce || hashes[tx.Hash()] {
				t.Fatal("sender/nonces not independently signed and unique")
			}
			hashes[tx.Hash()] = true
			nonce++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(hashes) != 8 {
		t.Fatal("unique corpus count")
	}
	for _, v := range m.Validators {
		original, err := os.ReadFile(v.Config)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(original, []byte("owner_queue")) {
			t.Fatal("default config changed")
		}
		var queue conf.Config
		err = conf.LoadConfigFromFile(filepath.Join(root, "queue-configs", "v"+string(rune('0'+v.Index))+".json"), &queue)
		if err != nil {
			t.Fatal(err)
		}
		if !queue.NodeCfg.PerformanceOwnerQueue || queue.NodeCfg.PerformanceQueueMaxTransactions != 163000 {
			t.Fatal("queue config not active")
		}
	}
	path := filepath.Join(root, "load", "queue", corpus.Shards[0].File)
	corrupted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupted[len(corrupted)-1] ^= 1
	if err := os.WriteFile(path, corrupted, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readQueueCorpus(m); err == nil {
		t.Fatal("changed signed payload accepted")
	}
	if err := provisionFixture(root, template, 4, 25000, base, true); err == nil {
		t.Fatal("existing fixture overwritten")
	}
}
func TestQueueFixtureRejectsShapeBeforeCreatingRoot(t *testing.T) {
	base, template := provisionTestPaths(t)
	root := filepath.Join(base, "bad")
	if err := provisionFixture(root, template, 64, 25000, base, true); err == nil {
		t.Fatal("large fixture accepted")
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatal("invalid fixture created data")
	}
}

func TestQueueCheckerRefusesNonzeroOwnerBeforeAdmission(t *testing.T) {
	base, template := provisionTestPaths(t)
	root := filepath.Join(base, "q")
	if err := provisionFixture(root, template, 4, 25000, base, true); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := readFixtureJSON(filepath.Join(root, "run.json"), &m); err != nil {
		t.Fatal(err)
	}
	_, digest, _, err := readQueueCorpus(m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(m.OwnerSocket)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", m.OwnerSocket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(m.OwnerSocket, 0600); err != nil {
		t.Fatal(err)
	}
	ingress, err := net.Listen("unix", filepath.Join(dir, "ingress.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ingress.Close()
	if err := os.Chmod(filepath.Join(dir, "ingress.sock"), 0600); err != nil {
		t.Fatal(err)
	}
	status := sharedexec.StrictIngressStatus{Identity: m.Identity, ManifestHash: digest, Epoch: [16]byte{1}, CanonicalHash: m.Identity.GenesisHash, NewlyAdmitted: 2, CryptoVerified: 2}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(status) })}
	go server.Serve(listener)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := queueSmokeFixture(ctx, filepath.Join(root, "run.json"), base); err == nil || !strings.Contains(err.Error(), "never blindly replay") {
		t.Fatalf("nonzero owner accepted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "queue-smoke-result.json")); !os.IsNotExist(err) {
		t.Fatal("failed queue checker published success")
	}
}
