package mptproof

import (
	"bytes"
	"context"
	"path/filepath"
	"sort"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mptbuild"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// g49FixtureAccount is one synthetic account entry for the tiny
// reth-format fixture below.
type g49FixtureAccount struct {
	addr  [20]byte
	value []byte // arbitrary opaque "account compact" bytes
	slots map[[32]byte][]byte
}

// g49WriteHashedTables opens (or creates) an MDBX env at dir and
// writes HashedAccounts + HashedStorages rows matching reth's wire
// layout: HashedAccounts[keccak(addr)] = value, HashedStorages
// DupSort[keccak(addr)] -> keccak(slot) || value. Keys/dups are
// written in strictly ascending order via Append/AppendDup, which is
// what reth itself guarantees and what RethHashedLeafSource assumes.
func g49WriteHashedTables(t *testing.T, dir string, accounts []g49FixtureAccount) {
	t.Helper()
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(1) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[rethHashedAccountsTable] = kv.TableCfgItem{}
			d[rethHashedStoragesTable] = kv.TableCfgItem{Flags: kv.DupSort}
			return d
		}).
		Open(context.Background())
	if err != nil {
		t.Fatalf("open mdbx fixture: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	defer tx.Rollback()

	type accRow struct {
		hAddr []byte
		value []byte
		slots map[[32]byte][]byte
	}
	rows := make([]accRow, 0, len(accounts))
	for _, a := range accounts {
		rows = append(rows, accRow{hAddr: keccak(a.addr[:]), value: a.value, slots: a.slots})
	}
	sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i].hAddr, rows[j].hAddr) < 0 })

	accCur, err := tx.RwCursor(rethHashedAccountsTable)
	if err != nil {
		t.Fatalf("RwCursor accounts: %v", err)
	}
	defer accCur.Close()
	for _, r := range rows {
		if err := accCur.Append(r.hAddr, r.value); err != nil {
			t.Fatalf("append HashedAccounts: %v", err)
		}
	}

	for _, r := range rows {
		if len(r.slots) == 0 {
			continue
		}
		type slotRow struct {
			hSlot []byte
			value []byte
		}
		srows := make([]slotRow, 0, len(r.slots))
		for slot, v := range r.slots {
			srows = append(srows, slotRow{hSlot: keccak(slot[:]), value: v})
		}
		sort.Slice(srows, func(i, j int) bool { return bytes.Compare(srows[i].hSlot, srows[j].hSlot) < 0 })
		for _, sr := range srows {
			dup := make([]byte, 0, len(sr.hSlot)+len(sr.value))
			dup = append(dup, sr.hSlot...)
			dup = append(dup, sr.value...)
			if err := tx.AppendDup(rethHashedStoragesTable, r.hAddr, dup); err != nil {
				t.Fatalf("append HashedStorages: %v", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
}

// g49BuildTrieFromAccounts builds AccountsTrie/StoragesTrie (via
// mptbuild, the real 3-pass pipeline) from the same accounts used to
// populate the HashedAccounts/HashedStorages fixture, so the two
// agree on every leaf value and the resulting roots are internally
// consistent.
func g49BuildTrieFromAccounts(t *testing.T, accDir, storDir string, accounts []g49FixtureAccount) {
	t.Helper()
	acctEntries := make([][2][]byte, 0, len(accounts))
	storEntries := make([][2][]byte, 0)
	for _, a := range accounts {
		acctEntries = append(acctEntries, [2][]byte{append([]byte{}, a.addr[:]...), append([]byte{}, a.value...)})
		for slot, v := range a.slots {
			sv := append(append([]byte{}, slot[:]...), v...)
			storEntries = append(storEntries, [2][]byte{append([]byte{}, a.addr[:]...), sv})
		}
	}
	if len(storEntries) == 0 {
		// mptbuild needs at least one entry.
		storEntries = append(storEntries, [2][]byte{append([]byte{}, accounts[0].addr[:]...), append(make([]byte, 32), 0xAB)})
	}

	atgt := &mptbuild.MDBXTarget{DBPath: accDir, Table: "AccountsTrie", MapSizeGB: 1}
	if _, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: acctEntries},
		Target:    atgt,
		Extractor: mptbuild.NewAccountExtractor(),
		TmpDir:    filepath.Join(accDir, "etl"),
		BufMB:     1,
	}); err != nil {
		atgt.Close()
		t.Fatalf("build AccountsTrie: %v", err)
	}
	atgt.Close()

	stgt := &mptbuild.MDBXTarget{DBPath: storDir, Table: "StoragesTrie", MapSizeGB: 1}
	if _, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: storEntries},
		Target:    stgt,
		Extractor: mptbuild.NewStorageExtractor(),
		TmpDir:    filepath.Join(storDir, "etl"),
		BufMB:     1,
	}); err != nil {
		stgt.Close()
		t.Fatalf("build StoragesTrie: %v", err)
	}
	stgt.Close()
}

func g49MkAddr(seed byte) [20]byte {
	var a [20]byte
	for i := range a {
		a[i] = seed + byte(i)*3
	}
	return a
}

func g49MkSlot(seed byte) [32]byte {
	var s [32]byte
	for i := range s {
		s[i] = seed + byte(i)*5
	}
	return s
}

// g49Fixture builds the full tiny reth-format fixture: HashedAccounts
// + HashedStorages MDBX tables and a matching AccountsTrie/StoragesTrie
// pair, then opens a Generator backed by RethHashedLeafSource.
func g49Fixture(t *testing.T, n int) (*Generator, *RethHashedLeafSource, []g49FixtureAccount) {
	t.Helper()
	tmp := t.TempDir()
	hashedDir := filepath.Join(tmp, "hashed")
	accDir := filepath.Join(tmp, "acc-trie")
	storDir := filepath.Join(tmp, "stor-trie")

	accounts := make([]g49FixtureAccount, n)
	for i := 0; i < n; i++ {
		addr := g49MkAddr(byte(i*7 + 1))
		val := make([]byte, 48)
		for k := range val {
			val[k] = byte((i+k)*11 ^ 0x3c)
		}
		slots := map[[32]byte][]byte{}
		// Give every account 2 storage slots so StoragesTrie is non-trivial.
		for j := 0; j < 2; j++ {
			slot := g49MkSlot(byte(i*3 + j + 1))
			sv := make([]byte, 24)
			for k := range sv {
				sv[k] = byte((i+j+k)*13 ^ 0x5a)
			}
			slots[slot] = sv
		}
		accounts[i] = g49FixtureAccount{addr: addr, value: val, slots: slots}
	}

	g49WriteHashedTables(t, hashedDir, accounts)
	g49BuildTrieFromAccounts(t, accDir, storDir, accounts)

	src, err := NewRethHashedLeafSource(hashedDir, 1)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	t.Cleanup(func() { src.Close() })

	g, err := New(Config{AccountsTrieDir: accDir, StorageTrieDir: storDir, Leaves: src})
	if err != nil {
		t.Fatalf("New generator: %v", err)
	}
	t.Cleanup(func() { g.Close() })

	return g, src, accounts
}

func TestRethHashed_LatestAccountProof_PresentAndAbsent(t *testing.T) {
	g, _, accounts := g49Fixture(t, 12)

	present := accounts[5]
	proof, err := g.LatestAccountProof(present.addr)
	if err != nil {
		t.Fatalf("LatestAccountProof present: %v", err)
	}
	if !proof.LeafFound {
		t.Fatal("expected leaf found for present account")
	}
	if !bytes.Equal(proof.LeafValue, present.value) {
		t.Errorf("leaf value mismatch: got %x want %x", proof.LeafValue, present.value)
	}

	var absent [20]byte
	for i := range absent {
		absent[i] = 0xEE
	}
	aproof, err := g.LatestAccountProof(absent)
	if err != nil {
		t.Fatalf("LatestAccountProof absent: %v", err)
	}
	if aproof.LeafFound {
		t.Fatal("expected leaf NOT found for absent account")
	}
}

func TestRethHashed_LatestProof_AccountAndStorage(t *testing.T) {
	g, _, accounts := g49Fixture(t, 10)
	target := accounts[3]
	slots := make([][32]byte, 0, len(target.slots)+1)
	for slot := range target.slots {
		slots = append(slots, slot)
	}
	// Add one absent slot.
	slots = append(slots, g49MkSlot(0xFF))

	lp, err := g.LatestProof(target.addr, slots)
	if err != nil {
		t.Fatalf("LatestProof: %v", err)
	}
	if !lp.Account.LeafFound {
		t.Fatal("account leaf should be found")
	}
	foundCount := 0
	for i, sp := range lp.Storages {
		if sp.LeafFound {
			foundCount++
			want := target.slots[sp.Slot]
			if !bytes.Equal(sp.LeafValue, want) {
				t.Errorf("slot %d value mismatch: got %x want %x", i, sp.LeafValue, want)
			}
		}
	}
	if foundCount != len(target.slots) {
		t.Errorf("expected %d found slots, got %d", len(target.slots), foundCount)
	}
}

func TestRethHashed_VerifyStandardProof_RoundTripAndTamper(t *testing.T) {
	g, _, accounts := g49Fixture(t, 1)
	target := accounts[0]

	proof, err := g.LatestAccountProof(target.addr)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := g.FullAccountProofBytes(proof)
	if err != nil {
		t.Fatalf("FullAccountProofBytes: %v", err)
	}
	val, found, err := VerifyStandardProof(pb, proof.StateRoot, proof.HashedAddr[:])
	if err != nil {
		t.Fatalf("VerifyStandardProof: %v", err)
	}
	if !found {
		t.Fatal("expected found")
	}
	if !bytes.Equal(val, target.value) {
		t.Errorf("value mismatch: got %x want %x", val, target.value)
	}

	// Tamper with the last proof node — a single flipped byte must
	// break verification (either an error, or found-but-wrong-value,
	// or not-found).
	tampered := make([][]byte, len(pb))
	for i, node := range pb {
		tampered[i] = append([]byte{}, node...)
	}
	last := tampered[len(tampered)-1]
	if len(last) > 0 {
		last[len(last)-1] ^= 0xFF
	}
	tval, tfound, terr := VerifyStandardProof(tampered, proof.StateRoot, proof.HashedAddr[:])
	if terr == nil && tfound && bytes.Equal(tval, target.value) {
		t.Fatal("tampered proof unexpectedly verified with the correct value")
	}
}

func TestRethHashed_ScannerFastPath_MatchesMapLeafSourceSlowPath(t *testing.T) {
	g, _, accounts := g49Fixture(t, 15)
	target := accounts[9]

	fastProof, err := g.LatestAccountProof(target.addr)
	if err != nil {
		t.Fatal(err)
	}
	fastPB, err := g.FullAccountProofBytes(fastProof)
	if err != nil {
		t.Fatalf("fast FullAccountProofBytes: %v", err)
	}

	// Build an equivalent MapLeafSource over the same account set and
	// swap it in: subLeavesByPrefix then falls back to the slow
	// ScanAccounts path instead of the HashedKeyScanner fast path.
	values := make(map[[20]byte][]byte, len(accounts))
	for _, a := range accounts {
		values[a.addr] = a.value
	}
	mapSrc := &MapLeafSource{Accounts: values}
	g.SetLeafSource(mapSrc)

	slowProof, err := g.LatestAccountProof(target.addr)
	if err != nil {
		t.Fatal(err)
	}
	slowPB, err := g.FullAccountProofBytes(slowProof)
	if err != nil {
		t.Fatalf("slow FullAccountProofBytes: %v", err)
	}

	if len(fastPB) != len(slowPB) {
		t.Fatalf("proof node count differs: fast=%d slow=%d", len(fastPB), len(slowPB))
	}
	for i := range fastPB {
		if !bytes.Equal(fastPB[i], slowPB[i]) {
			t.Errorf("node %d differs between fast and slow path", i)
		}
	}
}

func TestRethHashed_AccountValueAndStorageValue_DirectAPI(t *testing.T) {
	_, src, accounts := g49Fixture(t, 5)
	target := accounts[2]

	v, found, err := src.AccountValue(target.addr)
	if err != nil {
		t.Fatal(err)
	}
	if !found || !bytes.Equal(v, target.value) {
		t.Errorf("AccountValue mismatch: found=%v got=%x want=%x", found, v, target.value)
	}

	for slot, want := range target.slots {
		sv, sfound, serr := src.StorageValue(target.addr, slot)
		if serr != nil {
			t.Fatal(serr)
		}
		if !sfound || !bytes.Equal(sv, want) {
			t.Errorf("StorageValue mismatch for slot %x: found=%v got=%x want=%x", slot, sfound, sv, want)
		}
	}

	// Absent account.
	var absent [20]byte
	absent[0] = 0xFE
	_, found, err = src.AccountValue(absent)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected absent account not found")
	}

	// ScanAccounts/ScanStorage are unsupported on the hashed-keyed
	// source — callers must use HashedKeyScanner instead.
	if err := src.ScanAccounts(func(addr [20]byte, value []byte) error { return nil }); err != ErrPlainKeyScanUnsupported {
		t.Errorf("ScanAccounts: expected ErrPlainKeyScanUnsupported, got %v", err)
	}
	if err := src.ScanStorage(func(addr [20]byte, slot [32]byte, value []byte) error { return nil }); err != ErrPlainKeyScanUnsupported {
		t.Errorf("ScanStorage: expected ErrPlainKeyScanUnsupported, got %v", err)
	}
}
