package mptproof

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mptbuild"
	"github.com/n42blockchain/N42/internal/mpttrie"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

// buildDenseUnifiedTestDB builds a unified MDBX env containing
// AccountsTrie + AccountsDense for `nAccts` synthetic accounts, plus
// a StoragesTrie + StoragesDense (storage left empty if nStor=0).
// Returns the env path and the addr→value map for proof testing.
func buildDenseUnifiedTestDB(t *testing.T, nAccts int) (string, map[[20]byte][]byte, [32]byte) {
	t.Helper()
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "unified")
	logger := log.New()

	acctEntries, acctValues := makeAccountEntriesAndValues(nAccts)

	// Open destination env with both compact + dense tables declared.
	// Declare V2 tables too so that re-opening via OpenUnifiedDB (which
	// also declares them) doesn't trigger MDBX's "phantom DBI" quirk
	// for tables present in TableCfg but missing on disk — see
	// internal/mpttrie/dense_reader.go::Has for context.
	db, err := mdbxkv.NewMDBX(logger).
		Path(dst).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["AccountsTrie"] = kv.TableCfgItem{}
			d["StoragesTrie"] = kv.TableCfgItem{}
			d["Meta"] = kv.TableCfgItem{}
			d[mpttrie.AccountsDenseTable] = kv.TableCfgItem{}
			d[mpttrie.StoragesDenseTable] = kv.TableCfgItem{}
			d[mpttrie.AccountsDenseV2Table] = kv.TableCfgItem{}
			d[mpttrie.StoragesDenseV2Table] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}

	// Build: route both compact (existing inMemoryTarget) and dense
	// (DenseBranchSink → ETL → cursor.Append) into this env.
	denseColl := etl.NewCollector(
		"acct-dense",
		filepath.Join(tmp, "etl-dense"),
		etl.NewSortableBuffer(64*datasize.MB),
		logger,
	)
	defer denseColl.Close()

	compactColl := etl.NewCollector(
		"acct-compact",
		filepath.Join(tmp, "etl-compact"),
		etl.NewSortableBuffer(64*datasize.MB),
		logger,
	)
	defer compactColl.Close()

	// In-process target that collects compact branches into compactColl.
	tgt := &mapCollectorTarget{
		root:    [32]byte{},
		entries: compactColl,
	}

	res, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: acctEntries},
		Target:    tgt,
		Extractor: mptbuild.NewAccountExtractor(),
		TmpDir:    filepath.Join(tmp, "etl-build"),
		BufMB:     1,
		DenseBranchSink: func(keyHex []byte, stateMask, treeMask, _ uint16, slotData []byte) error {
			enc := trie.MarshalTrieNodeDense(stateMask, treeMask, slotData, nil)
			k := make([]byte, len(keyHex))
			copy(k, keyHex)
			return denseColl.Collect(k, enc)
		},
	})
	if err != nil {
		db.Close()
		t.Fatalf("Build: %v", err)
	}

	// Single Rw tx writes both compact and dense, plus Meta.
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := compactColl.Load(tx, "AccountsTrie", etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		tx.Rollback()
		db.Close()
		t.Fatalf("compact load: %v", err)
	}
	if err := denseColl.Load(tx, mpttrie.AccountsDenseTable, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		tx.Rollback()
		db.Close()
		t.Fatalf("dense load: %v", err)
	}
	tx.Put("Meta", []byte("accounts:state_root"), res.StateRoot[:])
	tx.Put("Meta", []byte("accounts:built_at"), []byte(time.Now().UTC().Format(time.RFC3339)))
	// Storage trie root = empty Merkle root marker; we don't build storage in this test.
	emptyRoot := [32]byte{0x56, 0xe8, 0x1f, 0x17, 0x1b, 0xcc, 0x55, 0xa6, 0xff, 0x83, 0x45, 0xe6, 0x92, 0xc0, 0xf8, 0x6e, 0x5b, 0x48, 0xe0, 0x1b, 0x99, 0x6c, 0xad, 0xc0, 0x01, 0x62, 0x2f, 0xb5, 0xe3, 0x63, 0xb4, 0x21}
	tx.Put("Meta", []byte("storage:state_root"), emptyRoot[:])
	if err := tx.Commit(); err != nil {
		db.Close()
		t.Fatalf("commit: %v", err)
	}
	db.Close()

	return dst, acctValues, res.StateRoot
}

// mapCollectorTarget is an mptbuild.Target that funnels compact
// branches into an ETL collector instead of an MDBX env.
type mapCollectorTarget struct {
	root    [32]byte
	entries *etl.Collector
}

func (m *mapCollectorTarget) Begin() error { return nil }
func (m *mapCollectorTarget) Append(key, value []byte) error {
	return m.entries.Collect(append([]byte{}, key...), append([]byte{}, value...))
}
func (m *mapCollectorTarget) Commit(root [32]byte) error { m.root = root; return nil }
func (m *mapCollectorTarget) Close() error              { return nil }

// TestProofBytes_DenseFastPath builds a small unified env with
// AccountsDense populated, opens it via the Generator (which should
// auto-detect dense and select the fast path), generates a USDC-shaped
// proof, and verifies via the standard oracle.
func TestProofBytes_DenseFastPath(t *testing.T) {
	dst, values, expectedRoot := buildDenseUnifiedTestDB(t, 500)

	leaves := &MapLeafSource{Accounts: values}
	g, err := New(Config{ChaindataDir: dst, Leaves: leaves})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close()

	if g.accountsDense == nil {
		t.Fatal("accountsDense not engaged — dense table empty or detection broken")
	}
	root, _ := g.AccountsTrieRoot()
	if root != expectedRoot {
		t.Fatalf("root mismatch: env=%x build=%x", root, expectedRoot)
	}

	// Pick a few addresses across the keccak space.
	var addrs [][20]byte
	for a := range values {
		addrs = append(addrs, a)
		if len(addrs) >= 5 {
			break
		}
	}

	for i, addr := range addrs {
		proof, err := g.LatestAccountProof(addr)
		if err != nil {
			t.Fatalf("idx=%d LatestAccountProof: %v", i, err)
		}
		pb, err := g.FullAccountProofBytes(proof)
		if err != nil {
			t.Fatalf("idx=%d FullAccountProofBytes: %v", i, err)
		}
		gotVal, found, verr := VerifyStandardProof(pb, root, proof.HashedAddr[:])
		if verr != nil {
			t.Fatalf("idx=%d oracle: %v", i, verr)
		}
		if !found {
			t.Errorf("idx=%d oracle: !found", i)
			continue
		}
		// Value should equal values[addr].
		if !bytesEqual(gotVal, values[addr]) {
			t.Errorf("idx=%d value mismatch: got %x want %x", i, gotVal, values[addr])
		}
		t.Logf("✓ idx=%d dense proof: %d nodes / %d bytes", i, len(pb), totalProofBytes(pb))
	}
}

func totalProofBytes(pb ProofBytes) int {
	n := 0
	for _, p := range pb {
		n += len(p)
	}
	return n
}
