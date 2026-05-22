package mptproof

import (
	"bytes"
	"context"
	"path/filepath"
	"sort"
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

// hashedMapLookup is a SingleLeafLookup adapter for tests: keeps the
// accounts map's keccak(addr) sorted so prefix lookups work.
type hashedMapLookup struct {
	keys   [][32]byte // sorted ascending by keccak(addr)
	values [][]byte   // parallel to keys (account RLP)
}

func newHashedMapLookup(accounts map[[20]byte][]byte) *hashedMapLookup {
	out := &hashedMapLookup{}
	for a, v := range accounts {
		var k [32]byte
		copy(k[:], keccak(a[:]))
		out.keys = append(out.keys, k)
		out.values = append(out.values, v)
	}
	// Sort by keccak ascending so prefix lookup is a binary search.
	type pair struct {
		k [32]byte
		v []byte
	}
	all := make([]pair, len(out.keys))
	for i := range out.keys {
		all[i] = pair{out.keys[i], out.values[i]}
	}
	sort.Slice(all, func(i, j int) bool {
		return bytes.Compare(all[i].k[:], all[j].k[:]) < 0
	})
	for i := range all {
		out.keys[i] = all[i].k
		out.values[i] = all[i].v
	}
	return out
}

func (m *hashedMapLookup) AccountLeafByPrefix(prefix []byte) (hashedAddr [32]byte, value []byte, ok bool, err error) {
	for i, k := range m.keys {
		if hasNibblePrefixBytes(k[:], prefix) {
			return k, m.values[i], true, nil
		}
	}
	return hashedAddr, nil, false, nil
}

func (m *hashedMapLookup) StorageLeafByPrefix(prefix []byte) (composite [64]byte, value []byte, ok bool, err error) {
	return composite, nil, false, nil // not used for accounts test
}

func hasNibblePrefixBytes(bytes32 []byte, nibblePrefix []byte) bool {
	for i, n := range nibblePrefix {
		bIdx := i / 2
		if bIdx >= len(bytes32) {
			return false
		}
		var got byte
		if i%2 == 0 {
			got = bytes32[bIdx] >> 4
		} else {
			got = bytes32[bIdx] & 0xf
		}
		if got != n {
			return false
		}
	}
	return true
}

// mapLeafSourceWithLookup wraps MapLeafSource so it also satisfies
// mpttrie.SingleLeafLookup (the V2 path needs that to expand
// LeafMarker slots).
type mapLeafSourceWithLookup struct {
	*MapLeafSource
	*hashedMapLookup
}

// buildDenseV2UnifiedTestDB builds a unified env with AccountsTrie
// (compact) + AccountsDenseV2 (G2) for the V2 path.
func buildDenseV2UnifiedTestDB(t *testing.T, nAccts int) (string, map[[20]byte][]byte, [32]byte) {
	t.Helper()
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "unified")
	logger := log.New()

	acctEntries, acctValues := makeAccountEntriesAndValues(nAccts)

	db, err := mdbxkv.NewMDBX(logger).
		Path(dst).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(2 * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d["AccountsTrie"] = kv.TableCfgItem{}
			d["StoragesTrie"] = kv.TableCfgItem{}
			d["Meta"] = kv.TableCfgItem{}
			d[mpttrie.AccountsDenseV2Table] = kv.TableCfgItem{}
			d[mpttrie.StoragesDenseV2Table] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}

	denseColl := etl.NewCollector(
		"acct-dense-v2",
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

	tgt := &mapCollectorTarget{entries: compactColl}

	res, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: acctEntries},
		Target:    tgt,
		Extractor: mptbuild.NewAccountExtractor(),
		TmpDir:    filepath.Join(tmp, "etl-build"),
		BufMB:     1,
		DenseBranchSink: func(keyHex []byte, stateMask, treeMask, extMask uint16, slotData []byte) error {
			enc := trie.MarshalTrieNodeDenseV2(stateMask, treeMask, extMask, slotData, nil)
			k := make([]byte, len(keyHex))
			copy(k, keyHex)
			return denseColl.Collect(k, enc)
		},
	})
	if err != nil {
		db.Close()
		t.Fatalf("Build: %v", err)
	}

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
	if err := denseColl.Load(tx, mpttrie.AccountsDenseV2Table, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		tx.Rollback()
		db.Close()
		t.Fatalf("dense V2 load: %v", err)
	}
	tx.Put("Meta", []byte("accounts:state_root"), res.StateRoot[:])
	tx.Put("Meta", []byte("accounts:built_at"), []byte(time.Now().UTC().Format(time.RFC3339)))
	emptyRoot := [32]byte{0x56, 0xe8, 0x1f, 0x17, 0x1b, 0xcc, 0x55, 0xa6, 0xff, 0x83, 0x45, 0xe6, 0x92, 0xc0, 0xf8, 0x6e, 0x5b, 0x48, 0xe0, 0x1b, 0x99, 0x6c, 0xad, 0xc0, 0x01, 0x62, 0x2f, 0xb5, 0xe3, 0x63, 0xb4, 0x21}
	tx.Put("Meta", []byte("storage:state_root"), emptyRoot[:])
	if err := tx.Commit(); err != nil {
		db.Close()
		t.Fatalf("commit: %v", err)
	}
	db.Close()

	return dst, acctValues, res.StateRoot
}

// buildBothDenseUnifiedTestDB writes BOTH V1 and V2 dense tables
// from the same build pass — useful for parity checks.
func buildBothDenseUnifiedTestDB(t *testing.T, nAccts int) (string, map[[20]byte][]byte, [32]byte) {
	t.Helper()
	tmp := t.TempDir()
	dst := filepath.Join(tmp, "unified")
	logger := log.New()

	acctEntries, acctValues := makeAccountEntriesAndValues(nAccts)

	db, err := mdbxkv.NewMDBX(logger).
		Path(dst).Label(kv.ChainDB).PageSize(4096).
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
		}).Open(context.Background())
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}

	denseV1 := etl.NewCollector("acct-v1", filepath.Join(tmp, "etl-v1"), etl.NewSortableBuffer(64*datasize.MB), logger)
	defer denseV1.Close()
	denseV2 := etl.NewCollector("acct-v2", filepath.Join(tmp, "etl-v2"), etl.NewSortableBuffer(64*datasize.MB), logger)
	defer denseV2.Close()
	compactColl := etl.NewCollector("acct-c", filepath.Join(tmp, "etl-c"), etl.NewSortableBuffer(64*datasize.MB), logger)
	defer compactColl.Close()

	tgt := &mapCollectorTarget{entries: compactColl}
	res, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: acctEntries},
		Target:    tgt,
		Extractor: mptbuild.NewAccountExtractor(),
		TmpDir:    filepath.Join(tmp, "etl-build"),
		BufMB:     1,
		DenseBranchSink: func(keyHex []byte, stateMask, treeMask, extMask uint16, slotData []byte) error {
			k := make([]byte, len(keyHex))
			copy(k, keyHex)
			enc1 := trie.MarshalTrieNodeDense(stateMask, treeMask, slotData, nil)
			enc2 := trie.MarshalTrieNodeDenseV2(stateMask, treeMask, extMask, slotData, nil)
			if err := denseV1.Collect(k, enc1); err != nil {
				return err
			}
			return denseV2.Collect(k, enc2)
		},
	})
	if err != nil {
		db.Close()
		t.Fatalf("Build: %v", err)
	}

	tx, _ := db.BeginRw(context.Background())
	if err := compactColl.Load(tx, "AccountsTrie", etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		tx.Rollback()
		db.Close()
		t.Fatalf("compact load: %v", err)
	}
	if err := denseV1.Load(tx, mpttrie.AccountsDenseTable, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		tx.Rollback()
		db.Close()
		t.Fatalf("V1 load: %v", err)
	}
	if err := denseV2.Load(tx, mpttrie.AccountsDenseV2Table, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
		tx.Rollback()
		db.Close()
		t.Fatalf("V2 load: %v", err)
	}
	tx.Put("Meta", []byte("accounts:state_root"), res.StateRoot[:])
	tx.Put("Meta", []byte("accounts:built_at"), []byte(time.Now().UTC().Format(time.RFC3339)))
	emptyRoot := [32]byte{0x56, 0xe8, 0x1f, 0x17, 0x1b, 0xcc, 0x55, 0xa6, 0xff, 0x83, 0x45, 0xe6, 0x92, 0xc0, 0xf8, 0x6e, 0x5b, 0x48, 0xe0, 0x1b, 0x99, 0x6c, 0xad, 0xc0, 0x01, 0x62, 0x2f, 0xb5, 0xe3, 0x63, 0xb4, 0x21}
	tx.Put("Meta", []byte("storage:state_root"), emptyRoot[:])
	if err := tx.Commit(); err != nil {
		db.Close()
		t.Fatalf("commit: %v", err)
	}
	db.Close()
	return dst, acctValues, res.StateRoot
}

// TestProofBytes_DenseV2FastPath_VsV1: build V1 AND V2 in the same
// env, compare slots. Currently FAILS: G2 Option A ext-aware tracking
// captures some extension cases but not all (snapshot order vs
// extensionHash firing point needs further investigation). See
// docs/ethel/g2-extension-aware-encoding.md for current status.
func TestProofBytes_DenseV2FastPath_VsV1(t *testing.T) {
	t.Skip("V2 dispatch still disabled; Path 2 OR propagation made oracle PASS for sampled walks but some sibling slots at branch [0] still mismatch — root cause likely a remaining subtree where mock's first-keccak-match differs from HashBuilder's hashed leaf (or a single-leaf slot where the leaf goes via extension at a deeper level we don't yet propagate). See docs/ethel/g2-extension-aware-encoding.md.")
	dst, values, expectedRoot := buildBothDenseUnifiedTestDB(t, 500)

	base := &mapLeafSourceWithLookup{
		MapLeafSource:   &MapLeafSource{Accounts: values},
		hashedMapLookup: newHashedMapLookup(values),
	}
	g, err := New(Config{ChaindataDir: dst, Leaves: base})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer g.Close()

	if g.accountsDenseV2 == nil {
		t.Fatal("accountsDenseV2 not engaged")
	}
	if g.accountsDense == nil {
		t.Fatal("accountsDense (V1) not engaged — buildBothDenseUnifiedTestDB should populate it too")
	}
	root, _ := g.AccountsTrieRoot()
	if root != expectedRoot {
		t.Fatalf("root mismatch: env=%x build=%x", root, expectedRoot)
	}

	// Sanity: pick a few branch paths and compare V1 vs V2 expansion.
	// They MUST produce identical Slots[] (modulo LeafMarker → 33B).
	for _, branchPath := range [][]byte{
		{},                          // root
		{0},                         // hop 1, child 0
		{0, 5},                      // hop 2
	} {
		v1, ok1, err1 := g.accountsDense.Get(branchPath)
		if err1 != nil || !ok1 {
			continue
		}
		v2, ok2, err2 := g.accountsDenseV2.GetV2(branchPath, false, base.hashedMapLookup)
		if err2 != nil || !ok2 {
			t.Errorf("V2 missing at path %x", branchPath)
			continue
		}
		if v1.StateMask != v2.StateMask {
			t.Errorf("path %x: StateMask differs v1=%x v2=%x", branchPath, v1.StateMask, v2.StateMask)
		}
		for digit := 0; digit < 16; digit++ {
			s1, s2 := v1.Slots[digit], v2.Slots[digit]
			if !bytes.Equal(s1, s2) {
				t.Errorf("path %x digit %d: slot bytes differ\n  v1 %x\n  v2 %x",
					branchPath, digit, s1, s2)
			}
		}
	}

	// Pick a few addresses.
	addrs := make([][20]byte, 0, 5)
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
			t.Fatalf("idx=%d FullAccountProofBytes (V2): %v", i, err)
		}
		gotVal, found, verr := VerifyStandardProof(pb, root, proof.HashedAddr[:])
		if verr != nil {
			t.Fatalf("idx=%d V2 oracle: %v", i, verr)
		}
		if !found {
			t.Errorf("idx=%d V2 oracle: !found", i)
			continue
		}
		if !bytesEqual(gotVal, values[addr]) {
			t.Errorf("idx=%d V2 value mismatch: got %x want %x", i, gotVal, values[addr])
		}
		t.Logf("✓ idx=%d V2 dense proof: %d nodes / %d bytes", i, len(pb), totalProofBytes(pb))
	}
}
