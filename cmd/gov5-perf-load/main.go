// gov5-perf-load measures one finite signed corpus on a fresh isolated chain.
// It never provisions data, starts nodes, claims hardware, or resets a chain.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/params"
)

const runtimeBase = "/data/blockchain/gov5-performance-5m/runtime"
const corpusSize = 64 * 512
const batchSize = 8192

type validator struct {
	Index     int    `json:"index"`
	Address   string `json:"address"`
	PeerID    string `json:"peer_id"`
	DataDir   string `json:"data_dir"`
	Config    string `json:"config"`
	APISocket string `json:"api_socket"`
	TCPPort   int    `json:"tcp_port"`
}
type manifest struct {
	SchemaVersion    int                 `json:"schema_version"`
	Status           string              `json:"status"`
	Root             string              `json:"root"`
	Genesis          string              `json:"genesis"`
	GenesisSHA256    string              `json:"genesis_sha256"`
	Identity         sharedexec.Identity `json:"identity"`
	OwnerDir         string              `json:"owner_dir"`
	OwnerSocket      string              `json:"owner_socket"`
	OwnerParallelEVM bool                `json:"owner_parallel_evm"`
	Validators       []validator         `json:"validators"`
	LoadKeys         string              `json:"load_keys"`
	Transactions     string              `json:"transactions"`
	GasPrice         string              `json:"gas_price"`
}

func privateJSON(path string, value any) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || resolved != path {
		return errors.New("absolute unaliased fixture file required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 2<<20 {
		return errors.New("small private owned fixture file required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("one fixture JSON value required")
	}
	return nil
}

// Nonces are interleaved across senders. Each sender has a different recipient,
// avoiding a single recipient-balance hotspot. This distribution differs from
// historical S103 and is not used as a promotion comparison.
func signedCorpus(g *conf.Genesis, encoded []string, nonces int) ([][]byte, string, error) {
	if g == nil || g.Config == nil || g.Config.Performance == nil || g.Config.ChainID == nil || !g.Config.ChainID.IsInt64() || g.Config.ChainID.Int64() != params.PerformanceChainID || len(encoded) < 1 || len(encoded) > 64 || nonces < 1 || nonces > 512 {
		return nil, "", errors.New("bounded dedicated corpus required")
	}
	keys := make([]*ecdsa.PrivateKey, len(encoded))
	recipients := make([]types.Address, len(encoded))
	seen := make(map[types.Address]bool)
	for i, value := range encoded {
		key, err := crypto.HexToECDSA(value)
		if err != nil {
			return nil, "", errors.New("invalid private sender key")
		}
		address := crypto.PubkeyToAddress(key.PublicKey)
		if _, exists := g.Alloc[address]; !exists || seen[address] {
			return nil, "", errors.New("unique genesis-funded senders required")
		}
		seen[address] = true
		keys[i] = key
		hash := crypto.Keccak256([]byte("gov5-load-recipient-v1"), address[:])
		recipients[i] = types.BytesToAddress(hash[12:])
	}
	for _, recipient := range recipients {
		if recipient == (types.Address{}) || recipient == g.Coinbase || seen[recipient] {
			return nil, "", errors.New("recipient collision")
		}
		seen[recipient] = true
	}
	signer := transaction.NewLondonSigner(g.Config.ChainID)
	result := make([][]byte, 0, len(keys)*nonces)
	hasher := sha256.New()
	var size [4]byte
	for nonce := 0; nonce < nonces; nonce++ {
		for i, key := range keys {
			tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{Nonce: uint64(nonce), To: &recipients[i], Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
			if err != nil {
				return nil, "", err
			}
			raw, err := tx.EthEncoded()
			if err != nil {
				return nil, "", err
			}
			binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
			_, _ = hasher.Write(size[:])
			_, _ = hasher.Write(raw)
			result = append(result, raw)
		}
	}
	return result, hex.EncodeToString(hasher.Sum(nil)), nil
}

type admissionClient interface {
	Status(context.Context) (sharedvalidator.StatusResult, error)
	Submit(context.Context, sharedvalidator.CanonicalProgress, [][]byte) (sharedvalidator.TransactionsResult, error)
}
type feedResult struct {
	Accepted        int          `json:"accepted_transactions"`
	Batches         int          `json:"accepted_batches"`
	HeadRejections  int          `json:"head_rejections"`
	Retries         int          `json:"head_retries"`
	SupplyExhausted bool         `json:"supply_exhausted"`
	Error           string       `json:"error,omitempty"`
	FinishedSeconds float64      `json:"finished_seconds"`
	StartedSeconds  float64      `json:"started_seconds"`
	Submissions     []submission `json:"submissions"`
}

type submission struct {
	Offset        int     `json:"offset"`
	Attempt       int     `json:"attempt"`
	BeginSeconds  float64 `json:"begin_seconds"`
	EndSeconds    float64 `json:"end_seconds"`
	SubmitSeconds float64 `json:"submit_seconds"`
	Outcome       string  `json:"outcome"`
}

// Only an explicit 409 guarantees no admission and permits the same batch to
// be tried again. Every other failure stops feeding; an ambiguous response is
// never blindly resent, even when the eventual canonical count might reveal it.
func feed(ctx context.Context, client admissionClient, corpus [][]byte, batch int) feedResult {
	var result feedResult
	if batch < 1 || batch > batchSize || len(corpus) == 0 || len(corpus) > corpusSize {
		result.Error = "invalid bounded feed"
		return result
	}
	feedStart := time.Now()
	for offset := 0; offset < len(corpus); offset += batch {
		end := offset + batch
		if end > len(corpus) {
			end = len(corpus)
		}
		accepted := false
		for attempt := 0; attempt < 10; attempt++ {
			timing := submission{Offset: offset, Attempt: attempt, BeginSeconds: time.Since(feedStart).Seconds()}
			callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			head, err := client.Status(callCtx)
			if err == nil {
				submitStart := time.Now()
				_, err = client.Submit(callCtx, head.CanonicalProgress, corpus[offset:end])
				timing.SubmitSeconds = time.Since(submitStart).Seconds()
			}
			cancel()
			timing.EndSeconds = time.Since(feedStart).Seconds()
			timing.Outcome = "accepted"
			if err != nil {
				timing.Outcome = err.Error()
			}
			result.Submissions = append(result.Submissions, timing)
			if err == nil {
				result.Accepted += end - offset
				result.Batches++
				accepted = true
				break
			}
			if errors.Is(err, sharedvalidator.ErrAdmissionHeadChanged) {
				result.HeadRejections++
				if attempt < 9 {
					result.Retries++
					continue
				}
			}
			result.Error = err.Error()
			return result
		}
		if !accepted {
			result.Error = "bounded admission retries exhausted"
			return result
		}
	}
	result.SupplyExhausted = true
	return result
}

type observation struct {
	Seconds float64                      `json:"seconds"`
	Status  sharedvalidator.StatusResult `json:"status"`
}
type crossObservation struct {
	Seconds  float64                        `json:"seconds"`
	Statuses []sharedvalidator.StatusResult `json:"statuses"`
}

func allStatus(ctx context.Context, clients []*sharedvalidator.APIClient) ([]sharedvalidator.StatusResult, error) {
	statuses := make([]sharedvalidator.StatusResult, len(clients))
	failures := make([]error, len(clients))
	var group sync.WaitGroup
	for i, client := range clients {
		group.Add(1)
		go func(i int, client *sharedvalidator.APIClient) {
			defer group.Done()
			statuses[i], failures[i] = client.Status(ctx)
		}(i, client)
	}
	group.Wait()
	for _, err := range failures {
		if err != nil {
			return nil, err
		}
	}
	return statuses, nil
}

func writeResult(path string, value any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	return json.NewEncoder(file).Encode(value)
}

func run(ctx context.Context, path string) error {
	root := filepath.Dir(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path != filepath.Join(root, "run.json") || !bytes.HasPrefix([]byte(root), []byte(runtimeBase+"/")) {
		return errors.New("dedicated fixture manifest required")
	}
	if _, err := os.Lstat(filepath.Join(root, "load-result.json")); !os.IsNotExist(err) {
		return errors.New("existing load result is never overwritten")
	}
	var m manifest
	if err := privateJSON(path, &m); err != nil {
		return err
	}
	if m.SchemaVersion != 1 || m.Root != root || len(m.Validators) != 7 || m.Genesis != filepath.Join(root, "genesis.json") || m.LoadKeys != filepath.Join(root, "load/keys.json") || m.Transactions != "ethereum-secp256k1" || m.GasPrice != "1" || !m.OwnerParallelEVM {
		return errors.New("unexpected fresh workload topology")
	}
	var g conf.Genesis
	if err := privateJSON(m.Genesis, &g); err != nil {
		return err
	}
	raw, err := os.ReadFile(m.Genesis)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != m.GenesisSHA256 || g.Config == nil || g.Config.Performance == nil || g.Config.ChainID == nil {
		return errors.New("full genesis binding mismatch")
	}
	profile, _ := params.ResolveExecutionProfile("n42")
	if err := g.Config.ValidatePerformanceSelection("private", profile); err != nil {
		return err
	}
	genesis, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		return err
	}
	if m.Identity != (sharedexec.Identity{Version: 1, Namespace: g.Config.Performance.NetworkNamespace, ChainID: g.Config.ChainID.String(), GenesisHash: genesis.Hash()}) {
		return errors.New("actual genesis identity mismatch")
	}
	var keys []string
	if err := privateJSON(m.LoadKeys, &keys); err != nil {
		return err
	}
	if len(keys) != 64 {
		return errors.New("this finite workload requires exactly 64 senders")
	}
	preSign := time.Now()
	corpus, digest, err := signedCorpus(&g, keys, 512)
	if err != nil {
		return err
	}
	preSignSeconds := time.Since(preSign).Seconds()
	clients := make([]*sharedvalidator.APIClient, 7)
	defer func() {
		for _, client := range clients {
			if client != nil {
				client.Close()
			}
		}
	}()
	for i, v := range m.Validators {
		if v.Index != i || v.APISocket != filepath.Join(root, fmt.Sprintf("v%d", i), "validator-rpc/validator.sock") {
			return errors.New("validator socket path mismatch")
		}
		resolved, err := filepath.EvalSymlinks(v.APISocket)
		if err != nil || resolved != v.APISocket {
			return errors.New("unaliased validator socket required")
		}
		info, err := os.Lstat(v.APISocket)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(owner.Uid) != os.Geteuid() || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
			return errors.New("private owned validator socket required")
		}
		clients[i] = sharedvalidator.NewAPIClient(v.APISocket, m.Identity)
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	initial, err := allStatus(checkCtx, clients)
	cancel()
	if err != nil {
		return err
	}
	for _, item := range initial {
		if item.CommittedExecutedTransactions != 0 || item.QueuedTransactions != 0 {
			return errors.New("fresh zero-count empty-queue fixture required; no recovery nonce assumptions")
		}
	}
	// No feed exists until pre-signing and the initial seven-counter check end.
	start := time.Now()
	windowCtx, stopFeed := context.WithDeadline(ctx, start.Add(20*time.Second))
	defer stopFeed()
	feedDone := make(chan feedResult, 1)
	go func() {
		feedStarted := time.Since(start).Seconds()
		value := feed(windowCtx, clients[0], corpus, batchSize)
		value.StartedSeconds = feedStarted
		value.FinishedSeconds = time.Since(start).Seconds()
		feedDone <- value
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	endTimer := time.NewTimer(time.Until(start.Add(20 * time.Second)))
	defer endTimer.Stop()
	samples := []observation{{Seconds: 0, Status: initial[0]}}
	var crosses []crossObservation
	nextCross := time.Second
	var measurementError string
window:
	for {
		select {
		case <-ctx.Done():
			measurementError = ctx.Err().Error()
			break window
		case <-endTimer.C:
			break window
		case <-ticker.C:
			callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			item, err := clients[0].Status(callCtx)
			cancel()
			if err != nil {
				measurementError = err.Error()
				break window
			}
			elapsed := time.Since(start)
			samples = append(samples, observation{Seconds: elapsed.Seconds(), Status: item})
			if item.CommittedExecutedTransactions < samples[len(samples)-2].Status.CommittedExecutedTransactions || item.CommittedExecutedTransactions > corpusSize {
				measurementError = "window canonical count regressed or exceeded corpus"
				break window
			}
			if elapsed >= nextCross {
				callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				statuses, err := allStatus(callCtx, clients)
				cancel()
				if err != nil {
					measurementError = err.Error()
					break window
				}
				crosses = append(crosses, crossObservation{Seconds: time.Since(start).Seconds(), Statuses: statuses})
				nextCross = elapsed + time.Second
			}
		}
	}
	stopFeed()
	fed := <-feedDone
	endCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	endStatus, endErr := clients[0].Status(endCtx)
	cancel()
	endSeconds := time.Since(start).Seconds()
	if endErr != nil {
		measurementError = endErr.Error()
	}
	drainDeadline := time.Now().Add(30 * time.Second)
	var final []sharedvalidator.StatusResult
	drained := false
	for time.Now().Before(drainDeadline) && ctx.Err() == nil {
		callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		statuses, err := allStatus(callCtx, clients)
		cancel()
		if err != nil {
			measurementError = err.Error()
			break
		}
		final = statuses
		drained = true
		for _, item := range statuses {
			if item.CanonicalProgress != statuses[0].CanonicalProgress || item.CommittedExecutedTransactions != corpusSize || item.QueuedTransactions != 0 {
				drained = false
			}
		}
		if drained {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	passed := measurementError == "" && fed.Error == "" && fed.Accepted == corpusSize && drained
	var firstProgress, lastProgress *float64
	previousCount := initial[0].CommittedExecutedTransactions
	for _, sample := range append(samples, observation{Seconds: endSeconds, Status: endStatus}) {
		if sample.Status.CommittedExecutedTransactions > previousCount {
			observed := sample.Seconds
			if firstProgress == nil {
				firstProgress = &observed
			}
			lastProgress = &observed
			previousCount = sample.Status.CommittedExecutedTransactions
		}
	}
	tps := float64(0)
	if endErr == nil && endStatus.CommittedExecutedTransactions >= initial[0].CommittedExecutedTransactions && endSeconds > 0 {
		tps = float64(endStatus.CommittedExecutedTransactions-initial[0].CommittedExecutedTransactions) / endSeconds
	}
	result := map[string]any{"kind": "finite-corpus-committed-executed-throughput-not-capacity", "passed": passed, "identity": m.Identity, "genesis_sha256": m.GenesisSHA256, "senders": 64, "nonces_per_sender": 512, "recipient_distribution": "one-distinct-recipient-per-sender", "corpus_transactions": corpusSize, "corpus_sha256_length_prefixed_wire": digest, "batch_size": batchSize, "entry_validator": 0, "pre_sign_seconds": preSignSeconds, "window_requested_seconds": 20, "window_observed_seconds": endSeconds, "window_start_wall_utc": start.UTC(), "initial": initial, "samples": samples, "cross_checks": crosses, "window_end": endStatus, "committed_executed_tps": tps, "feed": fed, "drain_limit_seconds": 30, "final": final, "measurement_error": measurementError, "limitation": "Finite corpus and single ingress; supply-limited exploratory throughput, not sustained capacity or historical S103 promotion. E1 trusted execution; Build+Import execute twice; reuse disabled."}
	result["first_canonical_progress_seconds"], result["last_canonical_progress_seconds"] = firstProgress, lastProgress
	if err := writeResult(filepath.Join(root, "load-result.json"), result); err != nil {
		return err
	}
	if !passed {
		return errors.New("finite workload failed; retained load-result.json records counters/admission errors")
	}
	return nil
}

func main() {
	path := flag.String("manifest", "", "Fresh dedicated fixture manifest")
	flag.Parse()
	if flag.NArg() != 0 || *path == "" {
		fmt.Fprintln(os.Stderr, "explicit --manifest required")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	if err := run(ctx, *path); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(filepath.Join(filepath.Dir(*path), "load-result.json"))
}
