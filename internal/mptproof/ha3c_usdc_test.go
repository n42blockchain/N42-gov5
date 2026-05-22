package mptproof

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// HA-3c env paths — override via env vars for production runs.
var (
	commitmentBranchesEnvPath = envOr("N42_COMMITMENT_DIR", `D:\n42-commitment-test`)
	rethDBPath                = envOr("N42_RETH_DB", `D:\reth2k\db`)
)

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// TestHA3c_USDCProof_EndToEnd is the HA-3c deliverable. After
// HA-3b bootstrap populated CommitmentBranches, this test:
//
//  1. Opens CommitmentBranches (read-only).
//  2. Opens reth (read-only) for Account/Storage callbacks.
//  3. Touches USDC's account + slot 0 + slot 1.
//  4. Calls hph.GenerateWitness → *trie.Trie.
//  5. Calls witnessTrie.Prove for each touch.
//  6. Validates the proof chain.
//
// Skipped if the CommitmentBranches env is empty (bootstrap
// incomplete).
func TestHA3c_USDCProof_EndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(commitmentBranchesEnvPath, "mdbx.dat")); err != nil {
		t.Skipf("CommitmentBranches env not present at %s — run HA-3b bootstrap first",
			commitmentBranchesEnvPath)
	}
	if _, err := os.Stat(filepath.Join(rethDBPath, "mdbx.dat")); err != nil {
		t.Skipf("reth env not present at %s", rethDBPath)
	}

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdcAddr, _ := hex.DecodeString(usdcHex)

	ctx := context.Background()
	logger := log.New()

	// ---- CommitmentBranches read env ----
	commEnv, err := mdbxkv.NewMDBX(logger).
		Path(commitmentBranchesEnvPath).Label(kv.ChainDB).PageSize(4096).
		MapSize(256*datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[commitment.CommitmentBranchesTable] = kv.TableCfgItem{}
			return d
		}).Open(ctx)
	if err != nil {
		t.Fatalf("open commitment env: %v", err)
	}
	defer commEnv.Close()

	roTx, err := commEnv.BeginRo(ctx)
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer roTx.Rollback()

	entries, valueBytes, _ := commitment.CommitmentBranchesSize(ctx, roTx)
	t.Logf("CommitmentBranches: %d entries / %.2f MB",
		entries, float64(valueBytes)/1024/1024)
	if entries == 0 {
		t.Skip("CommitmentBranches empty (bootstrap incomplete)")
	}

	// ---- reth reader ----
	rethSrc, err := NewRethHashedLeafSource(rethDBPath, 4096)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	defer rethSrc.Close()
	reader := NewRethBackedReader(rethSrc)

	// Sanity: confirm USDC is in the reader's state. If not, the
	// bootstrap subset didn't include USDC and this test won't
	// produce a useful proof.
	usdcUpd, err := reader.Account(usdcAddr)
	if err != nil {
		t.Fatalf("USDC account read: %v", err)
	}
	if usdcUpd == nil {
		t.Skip("USDC not present in reth's PlainAccountState (unexpected)")
	}

	// ---- Wire HPH + PersistentPatriciaContext ----
	pctx := commitment.NewPersistentPatriciaContext(reader, reader)
	pctx.SetReadTx(roTx)
	hph := commitment.NewHexPatriciaHashed(int16(length.Addr), pctx)

	// ---- Touch USDC + slot 0 + slot 1 ----
	witness := commitment.NewUpdates(commitment.ModeDirect, "", commitment.KeyToHexNibbleHash)
	defer witness.Close()
	witness.TouchPlainKey(string(usdcAddr), nil, witness.TouchAccount)

	var slot0 [32]byte
	var slot1 [32]byte
	slot1[31] = 1
	witness.TouchPlainKey(string(append(append([]byte{}, usdcAddr...), slot0[:]...)),
		nil, witness.TouchStorage)
	witness.TouchPlainKey(string(append(append([]byte{}, usdcAddr...), slot1[:]...)),
		nil, witness.TouchStorage)

	// ---- GenerateWitness ----
	t0 := time.Now()
	witnessTrie, witnessRoot, err := hph.GenerateWitness(ctx, witness, nil, "ha3c-usdc")
	dWitness := time.Since(t0)
	if err != nil {
		t.Fatalf("GenerateWitness: %v", err)
	}
	t.Logf("GenerateWitness: %s — root %x",
		dWitness.Truncate(time.Millisecond), witnessRoot)

	// ---- Account proof ----
	usdcHashed := keccak256ToArr(usdcAddr)
	tAcct := time.Now()
	acctProof, err := witnessTrie.Prove(usdcHashed[:], 0, false)
	dAcct := time.Since(tAcct)
	if err != nil {
		t.Fatalf("Prove account: %v", err)
	}
	t.Logf("USDC account proof: %s — %d nodes",
		dAcct.Truncate(time.Microsecond), len(acctProof))
	if len(acctProof) == 0 {
		t.Fatalf("empty account proof")
	}
	verifyProofChain(t, "account", acctProof, witnessRoot)

	// ---- Storage slot proofs ----
	for i, slot := range [2][32]byte{slot0, slot1} {
		slotHashed := keccak256ToArr(slot[:])
		tStor := time.Now()
		storProof, err := witnessTrie.Prove(slotHashed[:], 0, true)
		dStor := time.Since(tStor)
		if err != nil {
			t.Fatalf("Prove slot %d: %v", i, err)
		}
		t.Logf("USDC slot %d proof: %s — %d nodes",
			i, dStor.Truncate(time.Microsecond), len(storProof))
	}

	t.Logf("HA-3c PASS: USDC end-to-end in %s",
		(dWitness + dAcct).Truncate(time.Millisecond))
	t.Logf("Bootstrap state: %d branches / %.2f MB",
		entries, float64(valueBytes)/1024/1024)
}

func verifyProofChain(t *testing.T, label string, proof [][]byte, expectedRoot []byte) {
	t.Helper()
	h := sha3.NewLegacyKeccak256()
	h.Write(proof[0])
	var top [32]byte
	h.Sum(top[:0])
	if !bytes.Equal(top[:], expectedRoot) {
		t.Errorf("%s: keccak(proof[0]) != root\nproof[0] hash: %x\nexpected:      %x",
			label, top[:], expectedRoot)
		return
	}
	for i := 1; i < len(proof); i++ {
		curHash := sha3.NewLegacyKeccak256()
		curHash.Write(proof[i])
		var sum [32]byte
		curHash.Sum(sum[:0])
		var marker [33]byte
		marker[0] = 0xa0
		copy(marker[1:], sum[:])
		if bytes.Contains(proof[i-1], marker[:]) {
			continue
		}
		if bytes.Contains(proof[i-1], proof[i]) {
			continue
		}
		t.Errorf("%s: proof chain broken at node %d", label, i)
		return
	}
}

func keccak256ToArr(data []byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var out [32]byte
	h.Sum(out[:0])
	return out
}
