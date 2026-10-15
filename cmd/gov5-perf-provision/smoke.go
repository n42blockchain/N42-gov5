package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

func readFixtureJSON(path string, value any) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("fixture file must be absolute without aliases")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 2<<20 {
		return errors.New("fixture file must be small, private and owned")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("fixture file requires one JSON value")
	}
	return nil
}

// Interleave two nonces per sender so even the four-sender functional fixture
// crosses the parallel import threshold. No caller-provided sender is trusted.
func signedFunctionalBatch(g *conf.Genesis, keys []string) ([][]byte, error) {
	if g == nil || g.Config == nil || g.Config.Performance == nil || g.Config.ChainID == nil || !g.Config.ChainID.IsInt64() || g.Config.ChainID.Int64() != params.PerformanceChainID || len(keys) < 1 || len(keys) > 64 {
		return nil, errors.New("dedicated funded functional genesis required")
	}
	to := types.HexToAddress("0x00000000000000000000000000000000000000f0")
	signer := transaction.NewLondonSigner(g.Config.ChainID)
	seen := make(map[types.Address]bool)
	for _, encoded := range keys {
		key, err := crypto.HexToECDSA(encoded)
		if err != nil {
			return nil, errors.New("invalid private functional sender key")
		}
		address := crypto.PubkeyToAddress(key.PublicKey)
		if seen[address] {
			return nil, errors.New("duplicate functional sender key")
		}
		seen[address] = true
	}
	var batch [][]byte
	for nonce := uint64(0); nonce < 2; nonce++ {
		for _, encoded := range keys {
			key, err := crypto.HexToECDSA(encoded)
			if err != nil {
				return nil, errors.New("invalid private functional sender key")
			}
			if _, ok := g.Alloc[crypto.PubkeyToAddress(key.PublicKey)]; !ok {
				return nil, errors.New("functional sender is not genesis-funded")
			}
			tx, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{Nonce: nonce, To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
			if err != nil {
				return nil, err
			}
			wire, err := tx.EthEncoded()
			if err != nil {
				return nil, err
			}
			batch = append(batch, wire)
		}
	}
	return batch, nil
}

func smokeFixture(ctx context.Context, path, base string) error {
	root := filepath.Dir(path)
	if !strings.HasPrefix(root, base+string(filepath.Separator)) || path != filepath.Join(root, "run.json") {
		return errors.New("explicit dedicated fixture manifest required")
	}
	var m manifest
	if err := readFixtureJSON(path, &m); err != nil {
		return err
	}
	if m.SchemaVersion != 1 || m.Root != root || m.Genesis != filepath.Join(root, "genesis.json") || m.LoadKeys != filepath.Join(root, "load", "keys.json") || len(m.Validators) != 7 || m.Transactions != "ethereum-secp256k1" || m.GasPrice != "1" {
		return errors.New("unexpected fixture schema/topology")
	}
	if _, err := os.Lstat(filepath.Join(root, "smoke-result.json")); !os.IsNotExist(err) {
		return errors.New("existing smoke result is never overwritten")
	}
	var g conf.Genesis
	if err := readFixtureJSON(m.Genesis, &g); err != nil {
		return err
	}
	data, err := os.ReadFile(m.Genesis)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != m.GenesisSHA256 {
		return errors.New("full genesis file SHA mismatch")
	}
	if g.Config == nil || g.Config.Performance == nil || g.Config.ChainID == nil {
		return errors.New("explicit performance genesis required")
	}
	genesis, _, err := (&internal.GenesisBlock{GenesisConfig: &g}).ToBlock()
	if err != nil {
		return err
	}
	if g.Config == nil || g.Config.Performance == nil || m.Identity != (sharedexec.Identity{Version: 1, Namespace: g.Config.Performance.NetworkNamespace, ChainID: g.Config.ChainID.String(), GenesisHash: genesis.Hash()}) {
		return errors.New("actual genesis identity mismatch")
	}
	var keys []string
	if err := readFixtureJSON(m.LoadKeys, &keys); err != nil {
		return err
	}
	batch, err := signedFunctionalBatch(&g, keys)
	if err != nil {
		return err
	}
	clients := make([]*sharedvalidator.APIClient, 7)
	initial := make([]sharedvalidator.StatusResult, 7)
	defer func() {
		for _, c := range clients {
			if c != nil {
				c.Close()
			}
		}
	}()
	for i, v := range m.Validators {
		if v.Index != i || v.APISocket != filepath.Join(root, fmt.Sprintf("v%d", i), "validator-rpc", "validator.sock") {
			return errors.New("validator API path mismatch")
		}
		resolved, err := filepath.EvalSymlinks(v.APISocket)
		if err != nil || resolved != v.APISocket {
			return errors.New("validator socket cannot alias another endpoint")
		}
		info, err := os.Lstat(v.APISocket)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
			return errors.New("private owned validator socket required")
		}
		clients[i] = sharedvalidator.NewAPIClient(v.APISocket, m.Identity)
		initial[i], err = clients[i].Status(ctx)
		if err != nil {
			return err
		}
		if initial[i].CommittedExecutedTransactions != 0 {
			return errors.New("fresh zero-transaction fixture required; nonce-zero batch is never replayed")
		}
	}
	for _, client := range clients {
		for attempt := 0; attempt < 5; attempt++ {
			head, err := client.Status(ctx)
			if err != nil {
				return err
			}
			if head.CommittedExecutedTransactions != 0 {
				break
			}
			_, err = client.Submit(ctx, head.CanonicalProgress, batch)
			if err == nil {
				break
			}
			if !errors.Is(err, sharedvalidator.ErrAdmissionHeadChanged) {
				return err
			}
			if attempt == 4 {
				return errors.New("canonical head kept changing during small admission")
			}
		}
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		final := make([]sharedvalidator.StatusResult, 7)
		converged := true
		for i, client := range clients {
			final[i], err = client.Status(ctx)
			if err != nil {
				return err
			}
			if final[i].CommittedExecutedTransactions != uint64(len(batch)) {
				converged = false
			}
			if i > 0 && final[i].CanonicalProgress != final[0].CanonicalProgress {
				converged = false
			}
		}
		if converged {
			return writePrivate(filepath.Join(root, "smoke-result.json"), map[string]any{"kind": "signed-transaction-functional-check-not-tps", "identity": m.Identity, "genesis_sha256": m.GenesisSHA256, "unique_committed_executed_transactions": len(batch), "initial": initial, "final": final, "execution_trust": "accepted-trusted-execution-source", "build_then_import_executes_twice": true})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
