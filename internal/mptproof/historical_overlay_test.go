package mptproof

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/internal/historicalstate"
)

// g49BuildHistoryStore builds a tiny account+storage MPHF history
// coldstore under dir, mirroring internal/historicalstate's own test
// helper (unexported there, so duplicated here in miniature).
func g49BuildHistoryStore(t *testing.T, dir string, accounts map[[20]byte][]history.Change, storage map[[52]byte][]history.Change) {
	t.Helper()
	if len(accounts) > 0 {
		w, err := history.NewMPHFWriter(history.MPHFWriterOpts{
			BaseDir:  dir,
			Prefix:   "account",
			PageSize: 4,
			TmpDir:   dir + "/tmp-acc",
			KeyCount: len(accounts),
			EtlBufMB: 1,
		})
		if err != nil {
			t.Fatalf("NewMPHFWriter account: %v", err)
		}
		keys := make([][20]byte, 0, len(accounts))
		for k := range accounts {
			keys = append(keys, k)
		}
		sortKeys20(keys)
		for _, k := range keys {
			blob := history.PackHistory(nil, accounts[k])
			if err := w.Append(k[:], blob); err != nil {
				t.Fatalf("append account history: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close account writer: %v", err)
		}
	}
	if len(storage) > 0 {
		w, err := history.NewMPHFWriter(history.MPHFWriterOpts{
			BaseDir:  dir,
			Prefix:   "storage",
			PageSize: 4,
			TmpDir:   dir + "/tmp-stor",
			KeyCount: len(storage),
			EtlBufMB: 1,
		})
		if err != nil {
			t.Fatalf("NewMPHFWriter storage: %v", err)
		}
		keys := make([][52]byte, 0, len(storage))
		for k := range storage {
			keys = append(keys, k)
		}
		sortKeys52(keys)
		for _, k := range keys {
			blob := history.PackHistory(nil, storage[k])
			if err := w.Append(k[:], blob); err != nil {
				t.Fatalf("append storage history: %v", err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("close storage writer: %v", err)
		}
	}
}

func sortKeys20(keys [][20]byte) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && bytes.Compare(keys[j-1][:], keys[j][:]) > 0; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
}

func sortKeys52(keys [][52]byte) {
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && bytes.Compare(keys[j-1][:], keys[j][:]) > 0; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
}

// TestHistoricalLeafSource_WithRealHistory exercises the branches that
// need a non-nil historicalstate.Reader: AccountValue/StorageValue
// hitting a real record, ScanStorage overlay, and Close.
func TestHistoricalLeafSource_WithRealHistory(t *testing.T) {
	dir := t.TempDir()
	addr := g49MkAddr(1)
	slot := g49MkSlot(1)
	var compositeKey [52]byte
	copy(compositeKey[:20], addr[:])
	copy(compositeKey[20:], slot[:])

	g49BuildHistoryStore(t, dir,
		map[[20]byte][]history.Change{
			addr: {{Block: 10, Value: []byte{0xAA}}, {Block: 100, Value: []byte{0xBB}}},
		},
		map[[52]byte][]history.Change{
			compositeKey: {{Block: 10, Value: []byte{0xCC}}, {Block: 100, Value: []byte{0xDD}}},
		},
	)

	reader, err := historicalstate.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()

	base := &MapLeafSource{Accounts: map[[20]byte][]byte{addr: {0xFF}}}
	h := NewHistoricalLeafSource(base, reader, 150)

	v, ok, err := h.AccountValue(addr)
	if err != nil || !ok {
		t.Fatalf("AccountValue: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(v, []byte{0xBB}) {
		t.Errorf("AccountValue: got %x want BB (as-of block 150 uses the block-100 record)", v)
	}

	sv, sok, serr := h.StorageValue(addr, slot)
	if serr != nil || !sok {
		t.Fatalf("StorageValue: ok=%v err=%v", sok, serr)
	}
	if !bytes.Equal(sv, []byte{0xDD}) {
		t.Errorf("StorageValue: got %x want DD", sv)
	}

	// Close is a no-op but must not error.
	if err := h.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// ScanStorage overlay: base has one slot, history overrides it.
	base2 := &MapLeafSource{
		Accounts: map[[20]byte][]byte{addr: {0xFF}},
		Storage:  map[[20]byte]map[[32]byte][]byte{addr: {slot: {0x00}}},
	}
	h2 := NewHistoricalLeafSource(base2, reader, 150)
	var gotVal []byte
	err = h2.ScanStorage(func(a [20]byte, s [32]byte, value []byte) error {
		if a == addr && s == slot {
			gotVal = value
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotVal, []byte{0xDD}) {
		t.Errorf("ScanStorage overlay: got %x want DD", gotVal)
	}
}

// TestGenerator_HistoricalProof_AndValues wires a real history dir
// into Config.HistoryDir and exercises HistoricalAccountValue,
// HistoricalStorageValue and Generator.HistoricalProof end to end.
func TestGenerator_HistoricalProof_AndValues(t *testing.T) {
	// Build the trie fixture manually (rather than via g49Fixture)
	// so only ONE Generator ever opens these MDBX dirs — mpttrie.Open
	// takes an exclusive lock, so two concurrent Generators over the
	// same trie dir would deadlock/MDBX_BUSY.
	tmp := t.TempDir()
	hashedDir := filepath.Join(tmp, "hashed")
	accDir := filepath.Join(tmp, "acc-trie")
	storDir := filepath.Join(tmp, "stor-trie")

	accounts := make([]g49FixtureAccount, 6)
	for i := range accounts {
		addr := g49MkAddr(byte(i*7 + 1))
		val := []byte{byte(i), 0xAA}
		slot := g49MkSlot(byte(i + 1))
		accounts[i] = g49FixtureAccount{addr: addr, value: val, slots: map[[32]byte][]byte{slot: {byte(i), 0xBB}}}
	}
	g49WriteHashedTables(t, hashedDir, accounts)
	g49BuildTrieFromAccounts(t, accDir, storDir, accounts)
	src, err := NewRethHashedLeafSource(hashedDir, 1)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}

	target := accounts[1]
	var slot [32]byte
	for s := range target.slots {
		slot = s
		break
	}

	histDir := t.TempDir()
	var compositeKey [52]byte
	copy(compositeKey[:20], target.addr[:])
	copy(compositeKey[20:], slot[:])
	g49BuildHistoryStore(t, histDir,
		map[[20]byte][]history.Change{
			target.addr: {{Block: 5, Value: []byte{0x01}}},
		},
		map[[52]byte][]history.Change{
			compositeKey: {{Block: 5, Value: []byte{0x02}}},
		},
	)
	hreader, err := historicalstate.Open(histDir)
	if err != nil {
		t.Fatalf("Open history: %v", err)
	}
	defer hreader.Close()

	// Generator.New doesn't accept an externally-opened *Reader, so
	// reopen a second Generator sharing the same trie dirs + leaf
	// source but with HistoryDir set this time.
	g2, err := New(Config{AccountsTrieDir: accDir, StorageTrieDir: storDir, Leaves: src, HistoryDir: histDir})
	if err != nil {
		t.Fatalf("New with HistoryDir: %v", err)
	}
	defer g2.Close()

	av, aok, err := g2.HistoricalAccountValue(target.addr, 50)
	if err != nil {
		t.Fatalf("HistoricalAccountValue: %v", err)
	}
	if !aok || !bytes.Equal(av, []byte{0x01}) {
		t.Errorf("HistoricalAccountValue: ok=%v got=%x want=01", aok, av)
	}

	sv, sok, err := g2.HistoricalStorageValue(target.addr, slot, 50)
	if err != nil {
		t.Fatalf("HistoricalStorageValue: %v", err)
	}
	if !sok || !bytes.Equal(sv, []byte{0x02}) {
		t.Errorf("HistoricalStorageValue: ok=%v got=%x want=02", sok, sv)
	}

	hp, err := g2.HistoricalProof(HistoricalProofRequest{Address: target.addr, Slots: [][32]byte{slot}, BlockN: 50})
	if err != nil {
		t.Fatalf("HistoricalProof: %v", err)
	}
	if !bytes.Equal(hp.AccountValueAtBlockN, []byte{0x01}) {
		t.Errorf("HistoricalProof.AccountValueAtBlockN: got %x want 01", hp.AccountValueAtBlockN)
	}
	if len(hp.StorageProofs) != 1 || !bytes.Equal(hp.StorageProofs[0].ValueAtBlockN, []byte{0x02}) {
		t.Errorf("HistoricalProof.StorageProofs: got %+v", hp.StorageProofs)
	}

	// Without HistoryDir configured, HistoricalAccountValue/Storage/HistoricalProof must error cleanly.
	gNoHist, _, noHistAccounts := g49Fixture(t, 1)
	if _, _, err := gNoHist.HistoricalAccountValue(noHistAccounts[0].addr, 50); err == nil {
		t.Error("expected error: history not configured")
	}
	if _, err := gNoHist.HistoricalProof(HistoricalProofRequest{Address: noHistAccounts[0].addr}); err == nil {
		t.Error("expected error: HistoricalProof requires HistoryDir")
	}
}
