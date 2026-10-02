package commitment

// cmT_hph_accessors_test.go drives HexPatriciaHashed's deferred-update
// accessors, string/grid dumps, the HexTrieState* free functions, and the
// branch-cell-address extraction helpers through a real small Process run.

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/lib/common/length"
)

func cmTNewSeededHPH(t *testing.T) (*HexPatriciaHashed, *RecordingContext) {
	t.Helper()
	db := cmTOpenWarmupEnv(t)
	t.Cleanup(db.Close)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	t.Cleanup(tx.Rollback)

	reader := &inMemAccountReader{store: map[string]*Update{}}
	pctx := NewPersistentPatriciaContext(reader, nil)
	pctx.SetWriteTx(tx)
	rc := NewRecordingContext(pctx)
	hph := NewHexPatriciaHashed(length.Addr, rc)

	// Three distinct addresses (same shape as cmT_verify_test.go's working
	// fixture) reliably produce a real branch node at the root; two
	// addresses can collapse into a single leaf/extension with no BranchData
	// ever written.
	addrs := [][]byte{
		bytes.Repeat([]byte{0x11}, 20),
		bytes.Repeat([]byte{0x22}, 20),
		bytes.Repeat([]byte{0x33}, 20),
	}
	ub := NewUpdateBuilder()
	for _, a := range addrs {
		hexAddr := bytesToHexString(a)
		nonce := uint64(a[0])
		balance := uint64(a[0]) * 10
		ub.Balance(hexAddr, balance).Nonce(hexAddr, nonce)

		u := &Update{Flags: BalanceUpdate | NonceUpdate, Nonce: nonce}
		u.Balance.SetUint64(balance)
		reader.store[string(a)] = u
	}
	plainKeys, builderUpdates := ub.Build()
	updates := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, builderUpdates)
	t.Cleanup(updates.Close)

	if _, err := hph.Process(t.Context(), updates, "cmT-hph", nil, WarmupConfig{}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	return hph, rc
}

func TestHPH_SimpleAccessors(t *testing.T) {
	hph, _ := cmTNewSeededHPH(t)

	if hph.Variant() != VariantHexPatriciaTrie {
		t.Errorf("Variant: got %v, want %v", hph.Variant(), VariantHexPatriciaTrie)
	}

	var tracerCalls int
	hph.SetCollapseTracer(func(path []byte) { tracerCalls++ })
	// Not asserting tracerCalls fired (depends on trie shape); just exercise
	// the setter and ensure Process still runs cleanly with it set.

	var putBranchCalls int
	hph.SetPutBranchFn(func(updateKey []byte, branchData []byte) { putBranchCalls++ })

	if c := hph.Cache(); c != nil {
		t.Errorf("Cache: got %v, want nil (warmup cache not enabled)", c)
	}

	grid := hph.Grid()
	_ = grid // just exercise the accessor; shape is implementation detail.

	hph.PrintGrid() // must not panic

	hph.ResetContext(hph.ctx) // no-op re-assignment exercises the setter
	hph.Reset()
	_ = tracerCalls
	_ = putBranchCalls
}

func TestHPH_DeferredUpdates(t *testing.T) {
	hph, _ := cmTNewSeededHPH(t)

	// Freshly processed trie with defer disabled: no deferred updates pending.
	if hph.HasPendingDeferredUpdates() {
		t.Error("expected no pending deferred updates before enabling defer mode")
	}
	taken := hph.TakeDeferredUpdates()
	if len(taken) != 0 {
		t.Errorf("TakeDeferredUpdates before any deferral: got %d, want 0", len(taken))
	}

	hph.SetDeferBranchUpdates(true)
	hph.SetLeaveDeferredForCaller(true)

	// Touch a new account so Process has fold work to defer.
	addr := bytes.Repeat([]byte{0x44}, 20)
	ub := NewUpdateBuilder()
	ub.Balance(bytesToHexString(addr), 999)
	plainKeys, builderUpdates := ub.Build()
	updates := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, builderUpdates)
	defer updates.Close()

	if _, err := hph.Process(t.Context(), updates, "cmT-defer", nil, WarmupConfig{}); err != nil {
		t.Fatalf("Process with defer: %v", err)
	}

	// Whether or not this particular fold produced deferred entries, both
	// accessors and the apply/clear path must be safe to call.
	_ = hph.HasPendingDeferredUpdates()
	if err := hph.ApplyAndClearInlineDeferredUpdates(); err != nil {
		t.Fatalf("ApplyAndClearInlineDeferredUpdates: %v", err)
	}
	if hph.HasPendingDeferredUpdates() {
		t.Error("expected no pending deferred updates after ApplyAndClearInlineDeferredUpdates")
	}
}

func TestHexTrieState_Helpers(t *testing.T) {
	// MockState (not the MDBX-backed RecordingContext fixture): EncodeCurrentState
	// needs a trie built the same way hex_patricia_hashed_test.go's own
	// StateEncodeDecodeSetup test does, so the root cell round-trips cleanly.
	ms := NewMockState(t)
	plainKeys, updates := NewUpdateBuilder().
		Balance("f5", 4).
		Balance("ff", 900234).
		Balance("03", 7).
		Build()
	if err := ms.applyPlainUpdates(plainKeys, updates); err != nil {
		t.Fatalf("applyPlainUpdates: %v", err)
	}
	hph := NewHexPatriciaHashed(1, ms)
	upds := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
	defer upds.Close()
	if _, err := hph.Process(t.Context(), upds, "", nil, WarmupConfig{}); err != nil {
		t.Fatalf("Process: %v", err)
	}

	stateEnc, err := hph.EncodeCurrentState(nil)
	if err != nil {
		t.Fatalf("EncodeCurrentState: %v", err)
	}

	// HexTrieExtractStateRoot/HexTrieStateToString expect a different wire
	// format than EncodeCurrentState's raw output: an 18-byte header
	// (txNum uint64 BE | blockNum uint64 BE | stateLen uint16 BE) followed by
	// the state.Encode bytes. No production caller builds this today (it is
	// unused outside this package); construct it directly here.
	const wantTxNum, wantBlockNum = uint64(7), uint64(42)
	enc := make([]byte, 18+len(stateEnc))
	binary.BigEndian.PutUint64(enc[0:8], wantTxNum)
	binary.BigEndian.PutUint64(enc[8:16], wantBlockNum)
	binary.BigEndian.PutUint16(enc[16:18], uint16(len(stateEnc)))
	copy(enc[18:], stateEnc)

	rootHash, blockNum, txNum, err := HexTrieExtractStateRoot(enc)
	if err != nil {
		t.Fatalf("HexTrieExtractStateRoot: %v", err)
	}
	if len(rootHash) == 0 {
		t.Error("HexTrieExtractStateRoot: empty root hash")
	}
	if blockNum != wantBlockNum {
		t.Errorf("blockNum: got %d, want %d", blockNum, wantBlockNum)
	}
	if txNum != wantTxNum {
		t.Errorf("txNum: got %d, want %d", txNum, wantTxNum)
	}

	short, err := HexTrieStateToShortString(enc)
	if err != nil {
		t.Fatalf("HexTrieStateToShortString: %v", err)
	}
	if short == "" {
		t.Error("HexTrieStateToShortString: empty result")
	}

	full, err := HexTrieStateToString(enc)
	if err != nil {
		t.Fatalf("HexTrieStateToString: %v", err)
	}
	if full == "" {
		t.Error("HexTrieStateToString: empty result")
	}

	// Error paths: too-short input.
	if _, _, _, err := HexTrieExtractStateRoot(enc[:4]); err == nil {
		t.Error("HexTrieExtractStateRoot: expected error on short input")
	}
	if _, err := HexTrieStateToShortString(enc[:4]); err == nil {
		t.Error("HexTrieStateToShortString: expected error on short input")
	}
	if _, err := HexTrieStateToString(enc[:4]); err == nil {
		t.Error("HexTrieStateToString: expected error on short input")
	}
}

// TestExtractBranchCellAddresses_AndSkipCellFields exercises the branch-data
// walking helpers directly against a real recorded branch (from
// cmT_verify_test.go's recording pattern), covering both the
// account-address and no-match nibble paths.
func TestExtractBranchCellAddresses_AndSkipCellFields(t *testing.T) {
	_, rc := cmTNewSeededHPH(t)
	if len(rc.putBranches) == 0 {
		t.Fatal("expected at least one recorded branch write")
	}

	var sawAccounts, sawStorages bool
	for _, bw := range rc.putBranches {
		for nibble := 0; nibble < 16; nibble++ {
			accts, stors := extractBranchCellAddresses(bw.NewData, nibble)
			if len(accts) > 0 {
				sawAccounts = true
			}
			if len(stors) > 0 {
				sawStorages = true
			}
		}
	}
	if !sawAccounts {
		t.Error("expected at least one account address extracted across all branches/nibbles")
	}
	_ = sawStorages // this fixture has no storage slots; just exercise the branch.

	// skipCellFields: a zero fieldBits (no extension/account/storage/hash)
	// must advance by exactly 0 extra bytes beyond pos.
	data := []byte{0x00, 0x00, 0x00, 0x00}
	if got := skipCellFields(data, 2, 0); got != 2 {
		t.Errorf("skipCellFields with no fields set: got pos %d, want 2", got)
	}
}
