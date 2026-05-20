package historicalstate

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/history"
)

// productionStore is the canonical mainnet history coldstore used by
// integration tests. Tests below skip if absent so unit tests stay
// portable.
const productionStore = `D:\n42-history-full`

// --- unit tests against a synthetic store -----------------------------

// TestAccountAsOfSynthetic builds a tiny in-memory MPHF history for two
// accounts, then exercises AccountAsOf at boundary and interior blocks.
func TestAccountAsOfSynthetic(t *testing.T) {
	dir := t.TempDir()
	mustBuildSyntheticAccountStore(t, dir, map[[20]byte][]history.Change{
		mustAddr("0101010101010101010101010101010101010101"): {
			{Block: 100, Value: []byte{0xAA}},        // at start of block 100, value was 0xAA
			{Block: 500, Value: []byte{0xBB, 0xBB}},  // at start of block 500
			{Block: 1000, Value: []byte{0xCC}},       // at start of block 1000
		},
		mustAddr("0202020202020202020202020202020202020202"): {
			{Block: 50, Value: []byte{0xFF}},
		},
	})

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	addrA := mustAddr("0101010101010101010101010101010101010101")
	addrB := mustAddr("0202020202020202020202020202020202020202")
	addrC := mustAddr("0303030303030303030303030303030303030303") // never touched

	tests := []struct {
		name    string
		addr    [20]byte
		block   uint64
		wantOK  bool
		wantHex string
	}{
		// addrA: changes at 100, 500, 1000
		{"before-first-change", addrA, 50, false, ""},
		{"at-first-change", addrA, 100, true, "aa"},
		{"between-1st-2nd", addrA, 300, true, "aa"},
		{"at-second-change", addrA, 500, true, "bbbb"},
		{"between-2nd-3rd", addrA, 800, true, "bbbb"},
		{"at-third-change", addrA, 1000, true, "cc"},
		{"after-last-change", addrA, 2000, true, "cc"},

		// addrB: single entry
		{"single-entry-before", addrB, 10, false, ""},
		{"single-entry-at", addrB, 50, true, "ff"},
		{"single-entry-after", addrB, 999, true, "ff"},

		// addrC: never present in store
		{"absent-key", addrC, 100, false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, ok, err := r.AccountAsOf(tt.addr, tt.block)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("found: got %v, want %v (val=%x)", ok, tt.wantOK, val)
			}
			if tt.wantOK {
				wantBytes, _ := hex.DecodeString(tt.wantHex)
				if !bytes.Equal(val, wantBytes) {
					t.Fatalf("value: got %x, want %x", val, wantBytes)
				}
			}
		})
	}
}

func TestStorageAsOfSynthetic(t *testing.T) {
	dir := t.TempDir()
	addr := mustAddr("aabbccddeeff00112233445566778899aabbccdd")
	slot1 := mustSlot("0000000000000000000000000000000000000000000000000000000000000001")
	slot2 := mustSlot("0000000000000000000000000000000000000000000000000000000000000002")

	mustBuildSyntheticStorageStore(t, dir, map[[52]byte][]history.Change{
		composeStorKey(addr, slot1): {
			{Block: 100, Value: []byte{0x11}},
			{Block: 200, Value: []byte{0x22, 0x22}},
		},
		composeStorKey(addr, slot2): {
			{Block: 150, Value: []byte{0x33}},
		},
	})

	r, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	val, ok, err := r.StorageAsOf(addr, slot1, 250)
	if err != nil || !ok {
		t.Fatalf("slot1@250: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(val, []byte{0x22, 0x22}) {
		t.Fatalf("slot1@250 value: got %x, want 2222", val)
	}

	val, ok, _ = r.StorageAsOf(addr, slot1, 99)
	if ok {
		t.Fatalf("slot1@99: want absent, got %x", val)
	}

	val, ok, _ = r.StorageAsOf(addr, slot2, 200)
	if !ok || !bytes.Equal(val, []byte{0x33}) {
		t.Fatalf("slot2@200: got ok=%v val=%x, want ok=true val=33", ok, val)
	}

	// Different slot on same addr — must not collide.
	slot3 := mustSlot("0000000000000000000000000000000000000000000000000000000000000003")
	if _, ok, _ := r.StorageAsOf(addr, slot3, 1000); ok {
		t.Fatalf("slot3 should be absent")
	}
}

// --- integration tests against D:\n42-history-full --------------------

// integrationStore opens the production store or skips. Tests that
// follow are best-effort smoke checks; correctness vs. mainnet is
// covered by cmd/n42-history-verify against the freezer ground truth.
func integrationStore(t *testing.T) *Reader {
	t.Helper()
	if _, err := os.Stat(filepath.Join(productionStore, "account.mphf")); err != nil {
		t.Skipf("%s not present: skipping integration test", productionStore)
	}
	r, err := Open(productionStore)
	if err != nil {
		t.Fatalf("Open(%s): %v", productionStore, err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// TestProductionStore_Stats_Smoke confirms the store opens and reports
// non-zero key counts in both domains.
func TestProductionStore_Stats_Smoke(t *testing.T) {
	r := integrationStore(t)
	st := r.Stats()
	t.Logf("account: %d keys / %d pages", st.AccountKeys, st.AccountPageCount)
	t.Logf("storage: %d keys / %d pages", st.StorageKeys, st.StoragePageCount)
	if st.AccountKeys == 0 || st.StorageKeys == 0 {
		t.Fatal("expected non-empty domains in production store")
	}
}

// TestProductionStore_Account_AtSpecificBlocks queries a small set of
// well-known mainnet contract addresses at named block heights and
// asserts only structural properties (we don't have an oracle here for
// exact bytes; cmd/n42-history-verify owns correctness vs. freezer).
//
// Addresses chosen:
//   - USDC token (deployed at block 6082465)
//   - WETH9 token (deployed at block 4719568)
//   - vitalik.eth EOA (active since block ~46000)
func TestProductionStore_Account_AtSpecificBlocks(t *testing.T) {
	r := integrationStore(t)

	usdc := mustAddr("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	weth := mustAddr("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2")
	vitalik := mustAddr("d8da6bf26964af9d7eed9e03e53415d37aa96045")

	type query struct {
		name   string
		addr   [20]byte
		block  uint64
		wantOK bool // whether we expect history to have a record
	}

	queries := []query{
		// USDC deployed block 6,082,465. Before deploy, no entry exists.
		{"USDC@5000000-before-deploy", usdc, 5_000_000, false},
		// After deploy, AsOf returns OldValue at deployment (= empty bytes,
		// "didn't exist"). The MPHF lookup succeeds (ok=true) and we get
		// the deployment-block OldValue. Empty bytes are still a hit.
		{"USDC@7000000-after-deploy", usdc, 7_000_000, true},
		{"USDC@25000000-latest", usdc, 25_000_000, true},

		// WETH9 deployed block 4,719,568.
		{"WETH9@4000000-before-deploy", weth, 4_000_000, false},
		{"WETH9@5000000-after-deploy", weth, 5_000_000, true},
		{"WETH9@25000000-latest", weth, 25_000_000, true},

		// vitalik.eth — exact first-touch block isn't known a-priori; use
		// a deeper block where we're confident at least one entry exists.
		{"vitalik@25000000-latest", vitalik, 25_000_000, true},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			val, ok, err := r.AccountAsOf(q.addr, q.block)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if ok != q.wantOK {
				t.Errorf("found: got %v want %v (val=%x len=%d)", ok, q.wantOK, val, len(val))
				return
			}
			if ok {
				t.Logf("  value (%d bytes): 0x%s", len(val), shortHex(val))
			}
		})
	}
}

// TestProductionStore_ContractAccountSeparation confirms reth-style
// separation of account state from storage state, by counting account-
// history entries on contracts with different ether-holding patterns:
//
//   - USDC (pure ERC-20, no payable):  account state changes ONCE
//     (deployment only). Nonce/balance/code never change again.
//   - WETH9 (wraps ETH, payable):      account state changes EVERY
//     time someone deposits/withdraws (balance moves). Should be
//     in the millions for a 25M-block run.
//
// This is a structural correctness check, not a numeric tolerance test.
func TestProductionStore_ContractAccountSeparation(t *testing.T) {
	r := integrationStore(t)

	t.Run("USDC-singleton", func(t *testing.T) {
		addr := mustAddr("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
		changes, ok, err := r.AccountHistory(addr)
		if err != nil || !ok {
			t.Fatalf("AccountHistory: ok=%v err=%v", ok, err)
		}
		t.Logf("USDC: %d account-history entries (first=block %d)", len(changes), changes[0].Block)
		if len(changes) != 1 {
			t.Errorf("USDC (no payable) expected EXACTLY 1 entry, got %d", len(changes))
		}
	})

	t.Run("WETH9-payable-frequent", func(t *testing.T) {
		addr := mustAddr("c02aaa39b223fe8d0a0e5c4f27ead9083c756cc2")
		changes, ok, err := r.AccountHistory(addr)
		if err != nil || !ok {
			t.Fatalf("AccountHistory: ok=%v err=%v", ok, err)
		}
		t.Logf("WETH9: %d account-history entries (first=block %d, last=%d)",
			len(changes), changes[0].Block, changes[len(changes)-1].Block)
		if len(changes) < 1_000_000 {
			t.Errorf("WETH9 (payable, heavy use) expected millions of entries, got %d", len(changes))
		}
	})
}

// TestProductionStore_EOA_MultipleEntries checks that an actively
// transacting EOA has multiple account history entries (each tx
// updates nonce and/or balance).
func TestProductionStore_EOA_MultipleEntries(t *testing.T) {
	r := integrationStore(t)
	// Coinbase Prime hot wallet — one of the most active EOAs on
	// mainnet, expected to have thousands of entries.
	coinbase := mustAddr("71660c4005ba85c37ccec55d0c4493e66fe775d3")
	changes, ok, err := r.AccountHistory(coinbase)
	if err != nil {
		t.Fatalf("AccountHistory: %v", err)
	}
	if !ok {
		t.Skip("coinbase address not present — pick another active EOA")
	}
	t.Logf("active EOA history: %d entries (first=%d, last=%d)",
		len(changes), changes[0].Block, changes[len(changes)-1].Block)
	if len(changes) < 10 {
		t.Errorf("active EOA should have many entries, got %d", len(changes))
	}
	if bytes.Equal(changes[0].Value, changes[len(changes)-1].Value) {
		t.Errorf("active EOA: first==last value, suspicious")
	}
}

// TestProductionStore_Storage_Monotonic checks that an active storage
// slot's value differs between an early block and a late block. USDC
// totalSupply is the canonical example: it grows over time.
func TestProductionStore_Storage_Monotonic(t *testing.T) {
	r := integrationStore(t)
	usdc := mustAddr("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	totalSupplySlot := mustSlot("000000000000000000000000000000000000000000000000000000000000000b")
	changes, ok, err := r.StorageHistory(usdc, totalSupplySlot)
	if err != nil || !ok {
		t.Fatalf("USDC totalSupply history: ok=%v err=%v", ok, err)
	}
	t.Logf("USDC totalSupply history: %d entries", len(changes))
	if len(changes) < 100 {
		t.Errorf("USDC totalSupply should have many changes, got %d", len(changes))
	}
	if bytes.Equal(changes[0].Value, changes[len(changes)-1].Value) {
		t.Errorf("USDC totalSupply: first==last, suspicious")
	}
}

// TestProductionStore_Storage_AtSpecificBlocks queries known storage
// slots on known contracts at specific block heights.
func TestProductionStore_Storage_AtSpecificBlocks(t *testing.T) {
	r := integrationStore(t)

	usdc := mustAddr("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	// USDC totalSupply at slot keccak-mapped 0x0b (ERC-20 standard layout
	// varies, this is a sentinel slot expected to be active).
	slot0 := mustSlot("0000000000000000000000000000000000000000000000000000000000000000")
	slotB := mustSlot("000000000000000000000000000000000000000000000000000000000000000b")

	queries := []struct {
		name  string
		slot  [32]byte
		block uint64
	}{
		{"USDC/slot0@10000000", slot0, 10_000_000},
		{"USDC/slot0@20000000", slot0, 20_000_000},
		{"USDC/slot0@25000000", slot0, 25_000_000},
		{"USDC/slotB@10000000", slotB, 10_000_000},
		{"USDC/slotB@25000000", slotB, 25_000_000},
	}

	for _, q := range queries {
		t.Run(q.name, func(t *testing.T) {
			val, ok, err := r.StorageAsOf(usdc, q.slot, q.block)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			t.Logf("  ok=%v  value (%d bytes): 0x%s", ok, len(val), shortHex(val))
		})
	}
}

// TestProductionStore_PhantomAddr verifies that a high-entropy address
// that almost certainly does not exist in mainnet returns absent. This
// is the MPHF "phantom guard" check — the 4-byte fingerprint must catch
// strangers that the MPHF maps to a real ordinal.
func TestProductionStore_PhantomAddr(t *testing.T) {
	r := integrationStore(t)
	// Random high-entropy address (synthesised, never used on mainnet).
	phantom := mustAddr("deadc0debadbeef0deadc0debadbeef0deadc0de")
	val, ok, err := r.AccountAsOf(phantom, 25_000_000)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Errorf("phantom addr returned %d bytes — fingerprint guard failed", len(val))
	}
	_ = val
}

// --- helpers ----------------------------------------------------------

func mustAddr(s string) [20]byte {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 20 {
		panic("bad address: " + s)
	}
	var a [20]byte
	copy(a[:], b)
	return a
}

func mustSlot(s string) [32]byte {
	b, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
	if err != nil || len(b) != 32 {
		panic("bad slot: " + s)
	}
	var a [32]byte
	copy(a[:], b)
	return a
}

func composeStorKey(addr [20]byte, slot [32]byte) [52]byte {
	var k [52]byte
	copy(k[:20], addr[:])
	copy(k[20:], slot[:])
	return k
}

func shortHex(b []byte) string {
	if len(b) <= 16 {
		return hex.EncodeToString(b)
	}
	return hex.EncodeToString(b[:8]) + "..." + hex.EncodeToString(b[len(b)-8:])
}
