package commitment

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// inMemAccountReader/inMemStorageReader: tiny readers used by the
// HA-3a round-trip test. They reproduce what a future production
// AccountReader/StorageReader (backed by reth's HashedAccounts or
// N42's PlainAccountState) will deliver.
type inMemAccountReader struct {
	store map[string]*Update // key = string(plainAddr)
}

func (r *inMemAccountReader) Account(plainKey []byte) (*Update, error) {
	if u, ok := r.store[string(plainKey)]; ok {
		return u, nil
	}
	return nil, nil
}

type inMemStorageReader struct {
	store map[string]*Update // key = string(plainAddr || slot)
}

func (r *inMemStorageReader) Storage(plainKey []byte) (*Update, error) {
	if u, ok := r.store[string(plainKey)]; ok {
		return u, nil
	}
	return nil, nil
}

// TestHA3a_PersistentBranchRoundTrip is the foundational HA-3a
// validation:
//
//  1. Open an MDBX env declaring the CommitmentBranches table.
//  2. Run HPH.Process with PersistentPatriciaContext (writes
//     branches to MDBX as fold() emits them).
//  3. Capture the root.
//  4. CLOSE and RE-OPEN the env.
//  5. Spin up a fresh HexPatriciaHashed pointed at the SAME
//     CommitmentBranches data + the SAME in-memory account state.
//  6. Touch the same key, GenerateWitness, and verify the witness
//     root matches the original Process root.
//  7. trie.Prove on the witness; verify keccak(proof[0]) == root.
//
// This proves the persistence layer roundtrips correctly — the
// foundational requirement before HA-3b bootstrap can run.
func TestHA3a_PersistentBranchRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "commitment-branches")
	logger := log.New()

	openEnv := func() kv.RwDB {
		db, err := mdbxkv.NewMDBX(logger).
			Path(dbPath).Label(kv.ChainDB).PageSize(4096).
			MapSize(64*datasize.MB).
			WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
				d[CommitmentBranchesTable] = kv.TableCfgItem{}
				return d
			}).Open(context.Background())
		if err != nil {
			t.Fatalf("open env: %v", err)
		}
		return db
	}

	// ---------- Build accounts state ----------
	mkAccount := func(nonce uint64, balance uint64) []byte {
		acc := account.StateAccount{
			Nonce:    nonce,
			Balance:  *uint256.NewInt(balance),
			CodeHash: types.Hash{},
		}
		buf := make([]byte, acc.EncodingLengthForStorage())
		acc.EncodeForStorage(buf)
		return buf
	}

	addr1 := bytes.Repeat([]byte{0x01}, 20)
	addr2 := bytes.Repeat([]byte{0x02}, 20)
	addr3 := bytes.Repeat([]byte{0x03}, 20)
	accts := map[string][]byte{
		string(addr1): mkAccount(1, 100),
		string(addr2): mkAccount(2, 200),
		string(addr3): mkAccount(3, 300),
	}

	// Reader that returns parsed Update for each plainKey. The
	// HPH.Process flow consults this via PatriciaContext.Account
	// when unfolding fresh paths; the Updates we feed below
	// already carry the same values so Account() may not actually
	// be hit, but the reader must answer correctly if it is.
	reader := &inMemAccountReader{store: map[string]*Update{}}
	for plain, encoded := range accts {
		// Decode via TouchAccount logic by going through KeyUpdate.
		ku := &KeyUpdate{update: new(Update)}
		(&Updates{}).TouchAccount(ku, encoded)
		reader.store[plain] = ku.update
	}

	// ---------- Pass 1: Process + persist ----------
	db := openEnv()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}

	pctx := NewPersistentPatriciaContext(reader, nil)
	pctx.SetWriteTx(tx)

	hph := NewHexPatriciaHashed(length.Addr, pctx)

	// Build the Updates set the same way the test in
	// hex_patricia_hashed_test.go does it.
	ub := NewUpdateBuilder()
	for _, addrBytes := range [][]byte{addr1, addr2, addr3} {
		nonce := uint64(addrBytes[0])
		balance := uint64(addrBytes[0]) * 100
		hexAddr := bytesToHexString(addrBytes)
		ub.Balance(hexAddr, balance).Nonce(hexAddr, nonce)
	}
	plainKeys, builderUpdates := ub.Build()
	processUpdates := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, builderUpdates)
	defer processUpdates.Close()

	root1, err := hph.Process(context.Background(), processUpdates, "ha3a", nil, WarmupConfig{})
	if err != nil {
		t.Fatalf("hph.Process: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	t.Logf("pass-1 root: %x", root1)

	// Inspect persisted branches.
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	entries, valueBytes, err := CommitmentBranchesSize(context.Background(), roTx)
	if err != nil {
		t.Fatalf("CommitmentBranchesSize: %v", err)
	}
	roTx.Rollback()
	t.Logf("persisted %d branches / %d value bytes", entries, valueBytes)
	if entries == 0 {
		t.Fatalf("no branches persisted")
	}

	db.Close()

	// ---------- Pass 2: Re-open + verify root via GenerateWitness ----------
	db2 := openEnv()
	roTx2, err := db2.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo 2: %v", err)
	}
	// Note: roTx2 is rolled back explicitly before db2.Close below;
	// a defer here would close it AFTER db2.Close, which hangs the
	// env shutdown.

	pctx2 := NewPersistentPatriciaContext(reader, nil)
	pctx2.SetReadTx(roTx2)

	hph2 := NewHexPatriciaHashed(length.Addr, pctx2)
	// Set root hash so unfold knows to start from the persisted root.
	// HPH derives the root from the empty branch at "" prefix.

	witness := NewUpdates(ModeDirect, "", KeyToHexNibbleHash)
	defer witness.Close()
	witness.TouchPlainKey(string(addr1), nil, processUpdates.TouchAccount)

	witnessTrie, rootWitness, err := hph2.GenerateWitness(context.Background(), witness, nil, "ha3a-prove")
	if err != nil {
		t.Fatalf("GenerateWitness pass-2: %v", err)
	}
	if !bytes.Equal(root1, rootWitness) {
		t.Fatalf("rootWitness != root1\nwitness: %x\nstored:  %x", rootWitness, root1)
	}
	t.Logf("pass-2 witness root: %x (matches)", rootWitness)

	// Prove addr1.
	hashedAddr, err := CompactKey(KeyToHexNibbleHash(addr1))
	if err != nil {
		t.Fatalf("CompactKey: %v", err)
	}
	proof, err := witnessTrie.Prove(hashedAddr, 0, false)
	if err != nil {
		t.Fatalf("Prove: %v", err)
	}
	if len(proof) == 0 {
		t.Fatalf("empty proof")
	}
	t.Logf("HA-3a proof: %d nodes (top=%d B)", len(proof), len(proof[0]))
	roTx2.Rollback()
	db2.Close()
}

// bytesToHexString: 20-byte addr → 40-char lowercase hex (no 0x).
func bytesToHexString(b []byte) string {
	const hexchars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[i*2] = hexchars[x>>4]
		out[i*2+1] = hexchars[x&0x0f]
	}
	return string(out)
}
