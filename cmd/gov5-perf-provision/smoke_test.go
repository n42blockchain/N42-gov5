package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

func smokeTestFixture(t *testing.T) (string, manifest, conf.Genesis, []string) {
	t.Helper()
	base, template := provisionTestPaths(t)
	root := filepath.Join(base, "r")
	if err := provision(root, template, 4, 25000, base); err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := readFixtureJSON(filepath.Join(root, "run.json"), &m); err != nil {
		t.Fatal(err)
	}
	var g conf.Genesis
	if err := readFixtureJSON(m.Genesis, &g); err != nil {
		t.Fatal(err)
	}
	var keys []string
	if err := readFixtureJSON(m.LoadKeys, &keys); err != nil {
		t.Fatal(err)
	}
	return base, m, g, keys
}

func TestFunctionalBatchHasRealSignaturesAndOrderedPerSenderNonces(t *testing.T) {
	_, _, g, keys := smokeTestFixture(t)
	batch, err := signedFunctionalBatch(&g, keys)
	if err != nil || len(batch) != 8 {
		t.Fatalf("batch: %d %v", len(batch), err)
	}
	signer := transaction.NewLondonSigner(g.Config.ChainID)
	nonces := make(map[types.Address]uint64)
	for _, wire := range batch {
		tx, err := transaction.DecodeEthereumTransaction(wire)
		if err != nil {
			t.Fatal(err)
		}
		from, err := transaction.Sender(signer, tx)
		if err != nil {
			t.Fatal(err)
		}
		if tx.Nonce() != nonces[from] || tx.Gas() != 21000 {
			t.Fatal("batch lost per-sender ordering")
		}
		if _, exists := g.Alloc[from]; !exists {
			t.Fatal("recovered sender was not funded")
		}
		nonces[from]++
	}
	if len(nonces) != 4 {
		t.Fatal("batch collapsed independent senders")
	}
	for _, nonce := range nonces {
		if nonce != 2 {
			t.Fatal("sender did not sign both nonces")
		}
	}
	if _, err := signedFunctionalBatch(&g, []string{keys[0], keys[0]}); err == nil {
		t.Fatal("duplicate keys accepted")
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signedFunctionalBatch(&g, []string{strings.TrimPrefix(crypto.PubkeyToAddress(key.PublicKey).Hex(), "0x")}); err == nil {
		t.Fatal("invalid key accepted")
	}
	if _, err := signedFunctionalBatch(&g, []string{strings.Repeat("01", 32)}); err == nil {
		t.Fatal("unfunded key accepted")
	}
}

// This verifies the checker protocol/accounting, not a chain execution: actual
// Node/owner execution is covered separately and the OS-process fleet is pending.
func TestSmokeSevenUnixEndpointsCountOneChainAndPublishBoundResult(t *testing.T) {
	base, m, _, _ := smokeTestFixture(t)
	var admissions atomic.Int64
	var invalid atomic.Bool
	for _, v := range m.Validators {
		if err := os.Mkdir(filepath.Dir(v.APISocket), 0700); err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("unix", v.APISocket)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(v.APISocket, 0600); err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/status":
				progress := sharedvalidator.CanonicalProgress{Identity: m.Identity, BlockHash: m.Identity.GenesisHash}
				if admissions.Load() > 0 {
					progress.Number = 1
					progress.BlockHash = types.HexToHash("0x1234")
					progress.CommittedExecutedTransactions = 8
				}
				_ = json.NewEncoder(w).Encode(sharedvalidator.StatusResult{CanonicalProgress: progress})
			case "/v1/transactions":
				var request sharedvalidator.TransactionsRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Identity != m.Identity || request.CanonicalHash != m.Identity.GenesisHash || request.CanonicalNumber != 0 || len(request.Transactions) != 8 {
					invalid.Store(true)
					http.Error(w, "bad functional batch", 400)
					return
				}
				admissions.Add(1)
				_ = json.NewEncoder(w).Encode(sharedvalidator.TransactionsResult{Identity: m.Identity, CanonicalHash: request.CanonicalHash, CanonicalNumber: request.CanonicalNumber, Received: 8, QueuedTransactions: 8})
			default:
				invalid.Store(true)
				http.Error(w, "unknown path", 404)
			}
		})}
		go server.Serve(listener)
		t.Cleanup(func() { _ = server.Close() })
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := smokeFixture(ctx, filepath.Join(m.Root, "run.json"), base); err != nil {
		t.Fatal(err)
	}
	if admissions.Load() != 1 || invalid.Load() {
		t.Fatal("checker resent nonce-zero batch after observing finality")
	}
	var result struct {
		Kind  string                         `json:"kind"`
		Count int                            `json:"unique_committed_executed_transactions"`
		Final []sharedvalidator.StatusResult `json:"final"`
	}
	raw, err := os.ReadFile(filepath.Join(m.Root, "smoke-result.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Kind != "signed-transaction-functional-check-not-tps" || result.Count != 8 || len(result.Final) != 7 {
		t.Fatal("checker summed replica counters or mislabeled functional result")
	}
}

func TestSmokeRefusesPreviouslyCommittedFixtureBeforeTransactionAdmission(t *testing.T) {
	base, m, _, _ := smokeTestFixture(t)
	var admissions atomic.Int64
	v := m.Validators[0]
	if err := os.Mkdir(filepath.Dir(v.APISocket), 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", v.APISocket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(v.APISocket, 0600); err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/status" {
			admissions.Add(1)
			http.Error(w, "unexpected admission", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(sharedvalidator.StatusResult{CanonicalProgress: sharedvalidator.CanonicalProgress{Identity: m.Identity, Number: 1, BlockHash: types.HexToHash("0x1234"), CommittedExecutedTransactions: 8}})
	})}
	go server.Serve(listener)
	t.Cleanup(func() { _ = server.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := smokeFixture(ctx, filepath.Join(m.Root, "run.json"), base); err == nil || !strings.Contains(err.Error(), "never replayed") {
		t.Fatalf("used fixture: %v", err)
	}
	if admissions.Load() != 0 {
		t.Fatal("nonce-zero transactions were replayed after finality")
	}
	if _, err := os.Stat(filepath.Join(m.Root, "smoke-result.json")); !os.IsNotExist(err) {
		t.Fatal("failed smoke published result")
	}
}
