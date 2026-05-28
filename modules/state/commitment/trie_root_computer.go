// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TrieRootComputer: incremental Ethereum MPT root via CalcTrieRoot.
// Hashes changed keys into HashedAccounts/HashedStorage, builds a
// RetainList of dirty paths and invokes erigon2.7's FlatDBTrieLoader
// so TrieOfAccounts/TrieOfStorage subtrees are reused for unchanged
// paths. SetRwTx wires the MDBX write transaction used by the
// HashCollector callbacks to persist updated branch nodes.

package commitment

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// TrieRootComputer computes standard Ethereum MPT state roots incrementally
// using erigon2.7's CalcTrieRoot (FlatDBTrieLoader).
//
// Per-block flow:
//  1. Receive dirty accounts/storage from IntraBlockState
//  2. Hash changed keys → update HashedAccounts/HashedStorage
//  3. Build RetainList marking dirty hashed key paths
//  4. CalcTrieRoot reuses TrieOfAccounts/TrieOfStorage for unchanged subtrees
//  5. HashCollector callbacks update TrieOfAccounts/TrieOfStorage
//
// Two modes, selected via SetIncremental:
//
//   - incremental=false (default, legacy): ClearBucket TrieOf* before every
//     call, pass empty RetainList to CalcTrieRoot, HashCollectors rebuild
//     the full trie structure. Correct but O(state_size) per call.
//
//   - incremental=true: trust pre-populated TrieOf*, pass populated RetainList
//     (dirty paths) to CalcTrieRoot, HashCollectors emit put/delete only
//     for changed subtrees. O(dirty) per call. Requires TrieOf* to be
//     bootstrapped via a prior full-rebuild call (use BootstrapHPH).
type TrieRootComputer struct {
	tx          kv.RwTx
	hasher      hash256
	incremental bool

	// writeOnly: staged catch-up. ComputeRoot writes the dirty accounts/storage
	// to HashedAccounts/HashedStorage (so the next block reads them) but SKIPS
	// flushTrieRoot (CalcTrieRoot + TrieOf* update) and returns a zero hash. The
	// state root + TrieOf* maintenance are deferred to a per-sub-batch
	// MerkleStageIncremental, which builds a RetainList from the sub-batch's
	// changesets and runs ONE incremental FlatDBTrieLoader pass — memory bounded
	// by the sub-batch's touched keys (erigon2.7 IncrementIntermediateHashes
	// model). Removes the ~82ms/block dRoot from the execution stage.
	writeOnly bool
}

// SetWriteOnly toggles staged-catch-up write-only mode (per-block root deferred to
// MerkleStageIncremental). TrieOf* is NOT maintained per block while set.
func (t *TrieRootComputer) SetWriteOnly(v bool) { t.writeOnly = v }

type hash256 interface {
	Reset()
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func NewTrieRootComputer() *TrieRootComputer {
	return &TrieRootComputer{
		hasher: sha3.NewLegacyKeccak256(),
	}
}

// SetRwTx sets the read-write transaction for HashedAccounts/Storage updates.
func (t *TrieRootComputer) SetRwTx(tx kv.RwTx) {
	t.tx = tx
}

// SetIncremental toggles incremental mode. Default is false (legacy
// full-rebuild-per-call). See TrieRootComputer doc for requirements when
// enabling.
func (t *TrieRootComputer) SetIncremental(v bool) {
	t.incremental = v
}

var emptyCodeHash = crypto.Keccak256Hash(nil)

// ComputeRoot implements RootComputer.
// It incrementally updates HashedAccounts/HashedStorage from dirty keys,
// then calls CalcTrieRoot with a RetainList of changed paths.
func (t *TrieRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	if t.tx == nil {
		return types.Hash{}, fmt.Errorf("TrieRootComputer: tx not set")
	}

	rl := trie.NewRetainList(0)

	// Phase 1: Update HashedAccounts for dirty accounts + build RetainList.
	for addr, acct := range accounts {
		addrHash := t.keccakAddr(addr)

		// Add to RetainList WITH the "created" marker. CalcTrieRoot uses the
		// marker (via RetainWithMarker → nextCreated) to force a HashedAccounts
		// leaf scan around the key. This is REQUIRED for INSERTS: a brand-new
		// account sits at a nibble that the cached parent branch's hasState mask
		// doesn't know about, so the AccTrieCursor's child iteration never
		// descends to it; without the marker, SkipState stays true and the new
		// leaf is silently skipped (its child bit never added to hasState).
		// Modifies don't strictly need the marker (the key is already in
		// hasState, so _nextSiblingInMem clears SkipState), but marking all
		// dirty keys is correct and simpler — an extra bounded leaf scan is
		// harmless.
		rl.AddKeyWithMarker(addrHash[:], true)

		if acct == nil {
			// Account deleted.
			if err := t.tx.Delete(modules.HashedAccounts, addrHash[:]); err != nil {
				return types.Hash{}, err
			}
			// Also clean up storage for this account in HashedStorage.
			if err := t.deleteAccountStorage(addrHash); err != nil {
				return types.Hash{}, err
			}
			continue
		}

		// Normalize CodeHash.
		if acct.CodeHash == (types.Hash{}) {
			acct.CodeHash = emptyCodeHash
		}
		enc := acct.MarshalV2()
		if err := t.tx.Put(modules.HashedAccounts, addrHash[:], enc); err != nil {
			return types.Hash{}, err
		}
	}

	// Phase 2: Update HashedStorage for dirty storage slots.
	for addr, slots := range storage {
		addrHash := t.keccakAddr(addr)

		for slot, val := range slots {
			slotHash := t.keccakHash(slot)

			// compositeKey: addrHash(32) + slotHash(32) = 64B (incarnation removed)
			var compositeKey [64]byte
			copy(compositeKey[:32], addrHash[:])
			copy(compositeKey[32:], slotHash[:])

			// Add to RetainList with the "created" marker — same reason as
			// accounts: a brand-new storage slot must force a HashedStorage leaf
			// scan around its (cached-parent-unknown) nibble position.
			rl.AddKeyWithMarker(compositeKey[:], true)

			if val == nil || val.IsZero() {
				if err := t.tx.Delete(modules.HashedStorage, compositeKey[:]); err != nil {
					return types.Hash{}, err
				}
			} else {
				b := val.Bytes32()
				start := 0
				for start < 31 && b[start] == 0 {
					start++
				}
				if err := t.tx.Put(modules.HashedStorage, compositeKey[:], b[start:]); err != nil {
					return types.Hash{}, err
				}
			}
		}
	}

	// Staged catch-up: state written above; defer CalcTrieRoot + TrieOf* update to
	// the per-sub-batch MerkleStageIncremental. Returns zero (root not computed).
	if t.writeOnly {
		return types.Hash{}, nil
	}

	// N42_DROP_STALE_ROOT_CACHE=1: for every account whose storage was dirtied
	// this block, delete its 32-byte-key root-cache entry from TrieOfStorage
	// before flushTrieRoot. The FlatDBTrieLoader will then fall through to
	// HashedStorage leaf rebuild for that subtree instead of trusting the
	// stale cached root. Used to bisect #150: dump-tos confirmed EIP-2935's
	// cached root (`0x7b62992d...`) is stale relative to mainnet
	// `0xf88630...`; if dropping the cache yields the mainnet expected root,
	// the bug is incremental writes never propagate the new root back to
	// the keylen-32 record.
	if hexAddr := os.Getenv("N42_DROP_STALE_ADDR"); hexAddr != "" {
		// Scoped variant: drop ONLY the listed addrHash(es) (comma-separated
		// 64-hex). Used to isolate one contract's stale cache rather than
		// blow away the cache for every dirty-storage account.
		droppedEntries := 0
		droppedAccts := 0
		for _, item := range strings.Split(hexAddr, ",") {
			item = strings.TrimSpace(strings.TrimPrefix(item, "0x"))
			if len(item) != 64 {
				continue
			}
			ahBytes, derr := hex.DecodeString(item)
			if derr != nil {
				continue
			}
			cur, err := t.tx.Cursor(modules.TrieOfStorage)
			if err != nil {
				continue
			}
			var stale [][]byte
			for k, _, e := cur.Seek(ahBytes); k != nil && len(k) >= 32 && bytes.HasPrefix(k, ahBytes); k, _, e = cur.Next() {
				if e != nil {
					break
				}
				stale = append(stale, append([]byte(nil), k...))
			}
			cur.Close()
			for _, k := range stale {
				if err := t.tx.Delete(modules.TrieOfStorage, k); err == nil {
					droppedEntries++
				}
			}
			if len(stale) > 0 {
				droppedAccts++
			}
		}
		fmt.Fprintf(os.Stderr, "N42_DROP_STALE_ADDR droppedAccts=%d droppedEntries=%d\n", droppedAccts, droppedEntries)
	}

	root, err := t.flushTrieRoot(rl)
	if err != nil {
		return types.Hash{}, err
	}
	if os.Getenv("N42_DUMP160") != "" {
		t.diagnose160(accounts, storage, root)
	}
	return root, nil
}

// flushTrieRoot runs CalcTrieRoot over the given retain list and flushes the
// updated intermediate nodes to TrieOfAccounts/TrieOfStorage (Phase 3/4). Shared
// by the immediate (ComputeRoot) and deferred/batch (FinalizeRoot) paths.
func (t *TrieRootComputer) flushTrieRoot(rl *trie.RetainList) (types.Hash, error) {
	// Phase 3: CalcTrieRoot. In legacy mode, ClearBucket TrieOf* so the
	// rebuild is full (empty RetainList, every subtree recomputed from
	// HashedAccounts/HashedStorage). In incremental mode, leave TrieOf*
	// intact — the populated RetainList tells CalcTrieRoot which subtrees
	// to rebuild, and HashCollector callbacks update/delete only changed
	// nodes in TrieOf*.
	if !t.incremental {
		if err := t.tx.ClearBucket(modules.TrieOfAccounts); err != nil {
			return types.Hash{}, err
		}
		if err := t.tx.ClearBucket(modules.TrieOfStorage); err != nil {
			return types.Hash{}, err
		}
	}

	// Collect intermediate hashes in memory (avoid cursor read/write conflict).
	type kvPair struct{ k, v []byte }
	var accTrieUpdates []kvPair
	var storTrieUpdates []kvPair

	accCollector := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 {
			return nil
		}
		k := append([]byte{}, keyHex...)
		if hasState == 0 {
			accTrieUpdates = append(accTrieUpdates, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		accTrieUpdates = append(accTrieUpdates, kvPair{k, append([]byte{}, v...)})
		return nil
	}
	traceStor := os.Getenv("N42_TRACE_STORCOLL") == "1"
	storCollector := func(accWithInc []byte, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append(make([]byte, 0, len(accWithInc)+len(keyHex)), accWithInc...), keyHex...)
		if traceStor && len(accWithInc) >= 4 && accWithInc[0] == 0x6c && accWithInc[1] == 0x9d && accWithInc[2] == 0x57 && accWithInc[3] == 0xbe {
			// EIP-2935 contract: log every emission to verify root path
			// (keyHex==empty) is being written back.
			fmt.Fprintf(os.Stderr, "STORCOLL acc=%x keyHexLen=%d k=%x hasState=%04x hasTree=%04x hasHash=%04x nHashes=%d rootHashLen=%d\n",
				accWithInc[:4], len(keyHex), k[:min(len(k), 36)], hasState, hasTree, hasHash, len(hashes)/32, len(rootHash))
		}
		if len(k) == 0 {
			return nil
		}
		if hasState == 0 {
			storTrieUpdates = append(storTrieUpdates, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		storTrieUpdates = append(storTrieUpdates, kvPair{k, append([]byte{}, v...)})
		return nil
	}

	// Legacy mode: trie tables were cleared above, CalcTrieRoot does a
	// full rebuild with empty RetainList.
	// Incremental mode: pass the populated dirty-paths RetainList so
	// unchanged subtrees are read from TrieOf* instead of rebuilt.
	//
	// NOTE: incremental mode REQUIRES the empty-path (keylen-32) storage
	// "account.root" records to be present in TrieOfStorage. The loader
	// re-processes whole cached-IH ranges around any dirty path, recomputing
	// the leaves of neighbouring (untouched) accounts too — and those need
	// their cached storage root, which lives in the keylen-32 record. A
	// reth-migrated TrieOfStorage omits these, so it must be rebuilt once
	// (cmd/n42-rebuild-trie) before incremental updates are correct. See
	// trie_root_incremental_test.go for the regression covering this.
	retainer := trie.NewRetainList(0)
	if t.incremental {
		retainer = rl
	}
	loader := trie.NewFlatDBTrieLoader("trie-root", retainer, accCollector, storCollector, false)
	root, err := loader.CalcTrieRoot(t.tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}

	// Phase 4: Flush collected intermediate hashes to TrieOfAccounts/TrieOfStorage.
	for _, kv := range accTrieUpdates {
		if kv.v == nil {
			_ = t.tx.Delete(modules.TrieOfAccounts, kv.k)
		} else {
			if err := t.tx.Put(modules.TrieOfAccounts, kv.k, kv.v); err != nil {
				return types.Hash{}, err
			}
		}
	}
	for _, kv := range storTrieUpdates {
		// TrieOfStorage is a DupSort table keyed by addrHash+nibblePath: Put
		// APPENDS a new node-version under the path key instead of replacing the
		// existing one. Across blocks this accumulates stale node versions at the
		// same path, and StorageTrieCursor reads the lexicographically-first dup —
		// which can be an outdated node (e.g. one that predates a child added by a
		// later block), producing a wrong storage root whenever the current block's
		// dirty slots don't force a rescan of that child. Delete the path key first
		// (delAllDupData removes every stale version) so each path holds exactly the
		// current node. TrieOfAccounts is a plain table, so it is unaffected.
		if err := t.tx.Delete(modules.TrieOfStorage, kv.k); err != nil {
			return types.Hash{}, err
		}
		if kv.v != nil {
			if err := t.tx.Put(modules.TrieOfStorage, kv.k, kv.v); err != nil {
				return types.Hash{}, err
			}
		}
	}

	return root, nil
}

// diagnose160 isolates an incremental-root divergence (e.g. block 160). Pass A
// recomputes per-account storage roots from the CURRENT (cached/incremental)
// TrieOf*; pass B deletes the storage-changed accounts' TrieOfStorage and
// recomputes — a pure leaf rebuild (correct by construction). Diffing the two
// pins whether the bug is in a storage subtree's cached-IH read or in the
// account trie, and names the offending account.
func (t *TrieRootComputer) diagnose160(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
	incRoot types.Hash,
) {
	log.Warn("DIAG160 begin", "incRoot", incRoot.Hex(), "nAcct", len(accounts), "nStorAcct", len(storage))

	rootA, perA, nodesA, err := t.calcRootCapture(t.buildRetainList(accounts, storage))
	if err != nil {
		log.Warn("DIAG160 passA err", "err", err)
		return
	}

	for addr := range storage {
		ah := t.keccakAddr(addr)
		if e := t.deleteTrieOfStoragePrefix(ah[:]); e != nil {
			log.Warn("DIAG160 del err", "err", e)
			return
		}
	}
	rootB, perB, nodesB, err := t.calcRootCapture(t.buildRetainList(accounts, storage))
	if err != nil {
		log.Warn("DIAG160 passB err", "err", err)
		return
	}

	log.Warn("DIAG160 roots", "A_cached", rootA.Hex(), "B_leafrebuild", rootB.Hex(), "match", rootA == rootB)

	for addr := range storage {
		ah := t.keccakAddr(addr)
		a, b := perA[string(ah[:])], perB[string(ah[:])]
		if bytes.Equal(a, b) {
			continue
		}
		log.Warn("DIAG160 STORAGE-ROOT DIFF",
			"addrHash", hex.EncodeToString(ah[:]),
			"cached", hex.EncodeToString(a),
			"leafrebuild", hex.EncodeToString(b),
			"changedSlots", len(storage[addr]),
			"totalSlotsCapped", t.countStorageLeaves(ah[:], 256))

		// Dump the changed slots (hashed slot key + new value) for offline replay.
		for slot, val := range storage[addr] {
			sh := t.keccakHash(slot)
			vs := "0"
			if val != nil && !val.IsZero() {
				vs = val.Hex()
			}
			log.Warn("DIAG160   slot", "slotHash", hex.EncodeToString(sh[:]), "rawSlot", slot.Hex(), "newVal", vs)
		}

		// Dump per-path emitted nodes in both passes; mark where they diverge.
		pfx := string(ah[:]) + "|"
		paths := map[string]struct{}{}
		for k := range nodesA {
			if strings.HasPrefix(k, pfx) {
				paths[k[len(pfx):]] = struct{}{}
			}
		}
		for k := range nodesB {
			if strings.HasPrefix(k, pfx) {
				paths[k[len(pfx):]] = struct{}{}
			}
		}
		ordered := make([]string, 0, len(paths))
		for p := range paths {
			ordered = append(ordered, p)
		}
		sort.Slice(ordered, func(i, j int) bool {
			if len(ordered[i]) != len(ordered[j]) {
				return len(ordered[i]) < len(ordered[j])
			}
			return ordered[i] < ordered[j]
		})
		for _, p := range ordered {
			na, okA := nodesA[pfx+p]
			nb, okB := nodesB[pfx+p]
			same := okA && okB && na.hasState == nb.hasState && na.hasTree == nb.hasTree &&
				na.hasHash == nb.hasHash && bytes.Equal(na.hashes, nb.hashes) && bytes.Equal(na.rootHash, nb.rootHash)
			if same {
				continue
			}
			log.Warn("DIAG160   NODE-DIFF path="+nibStr([]byte(p)),
				"A", fmtNode(okA, na), "B", fmtNode(okB, nb))
		}
	}
}

func fmtNode(ok bool, n diagNode) string {
	if !ok {
		return "<absent>"
	}
	rh := "-"
	if len(n.rootHash) > 0 {
		rh = hex.EncodeToString(n.rootHash[:4])
	}
	out := fmt.Sprintf("hs=%04x ht=%04x hh=%04x nH=%d rh=%s", n.hasState, n.hasTree, n.hasHash, len(n.hashes)/32, rh)
	for i := 0; i+32 <= len(n.hashes); i += 32 {
		out += " " + hex.EncodeToString(n.hashes[i:i+4])
	}
	return out
}

func (t *TrieRootComputer) buildRetainList(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) *trie.RetainList {
	rl := trie.NewRetainList(0)
	for addr := range accounts {
		ah := t.keccakAddr(addr)
		rl.AddKeyWithMarker(ah[:], true)
	}
	for addr, slots := range storage {
		ah := t.keccakAddr(addr)
		for slot := range slots {
			sh := t.keccakHash(slot)
			var ck [64]byte
			copy(ck[:32], ah[:])
			copy(ck[32:], sh[:])
			rl.AddKeyWithMarker(ck[:], true)
		}
	}
	return rl
}

// calcRootCapture runs CalcTrieRoot (read-only: collectors do not persist) and
// captures each processed account's storage root (the empty-path account.root
// record) keyed by accWithInc.
type diagNode struct {
	hasState, hasTree, hasHash uint16
	hashes, rootHash           []byte
}

func (t *TrieRootComputer) calcRootCapture(rl *trie.RetainList) (types.Hash, map[string][]byte, map[string]diagNode, error) {
	perAcct := make(map[string][]byte)
	allNodes := make(map[string]diagNode)
	accColl := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error { return nil }
	storColl := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 {
			perAcct[string(accWithInc)] = append([]byte{}, rootHash...)
		}
		allNodes[string(accWithInc)+"|"+string(keyHex)] = diagNode{
			hasState, hasTree, hasHash,
			append([]byte{}, hashes...), append([]byte{}, rootHash...),
		}
		return nil
	}
	loader := trie.NewFlatDBTrieLoader("diag160", rl, accColl, storColl, false)
	root, err := loader.CalcTrieRoot(t.tx, nil)
	return root, perAcct, allNodes, err
}

// nibStr renders a nibble path (each byte 0..15) as a hex string.
func nibStr(keyHex []byte) string {
	const hd = "0123456789abcdef"
	b := make([]byte, len(keyHex))
	for i, n := range keyHex {
		b[i] = hd[n&0xf]
	}
	return string(b)
}

func (t *TrieRootComputer) deleteTrieOfStoragePrefix(addrHash []byte) error {
	c, err := t.tx.Cursor(modules.TrieOfStorage)
	if err != nil {
		return err
	}
	defer c.Close()
	var keys [][]byte
	for k, _, e := c.Seek(addrHash); k != nil && bytes.HasPrefix(k, addrHash); k, _, e = c.Next() {
		if e != nil {
			return e
		}
		keys = append(keys, append([]byte(nil), k...))
	}
	for _, k := range keys {
		if e := t.tx.Delete(modules.TrieOfStorage, k); e != nil {
			return e
		}
	}
	return nil
}

func (t *TrieRootComputer) countStorageLeaves(addrHash []byte, cap int) int {
	c, err := t.tx.Cursor(modules.HashedStorage)
	if err != nil {
		return -1
	}
	defer c.Close()
	n := 0
	for k, _, e := c.Seek(addrHash); k != nil && bytes.HasPrefix(k, addrHash); k, _, e = c.Next() {
		if e != nil {
			return -1
		}
		n++
		if n >= cap {
			break
		}
	}
	return n
}

// InitFromPlainState populates HashedAccounts + HashedStorage from PlainState.
// Called once at genesis or when resuming from empty hashed tables.
func (t *TrieRootComputer) InitFromPlainState() error {
	if t.tx == nil {
		return fmt.Errorf("TrieRootComputer: tx not set")
	}

	// Clear existing.
	for _, bucket := range []string{modules.HashedAccounts, modules.HashedStorage, modules.TrieOfAccounts, modules.TrieOfStorage} {
		if err := t.tx.ClearBucket(bucket); err != nil {
			return err
		}
	}

	// Hash all accounts.
	ac, err := t.tx.Cursor(modules.Account)
	if err != nil {
		return err
	}
	defer ac.Close()

	for k, v, err := ac.First(); k != nil; k, v, err = ac.Next() {
		if err != nil {
			return err
		}
		if len(k) != 20 {
			continue
		}
		var addr types.Address
		copy(addr[:], k)
		addrHash := t.keccakAddr(addr)
		if err := t.tx.Put(modules.HashedAccounts, addrHash[:], v); err != nil {
			return err
		}
	}

	// Hash all storage.
	sc, err := t.tx.Cursor(modules.Storage)
	if err != nil {
		return err
	}
	defer sc.Close()

	for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
		if err != nil {
			return err
		}
		if len(k) < 52 || len(v) == 0 {
			continue
		}
		var addr types.Address
		copy(addr[:], k[:20])
		addrHash := t.keccakAddr(addr)

		var slot types.Hash
		copy(slot[:], k[20:52])
		slotHash := t.keccakHash(slot)

		var compositeKey [64]byte
		copy(compositeKey[:32], addrHash[:])
		copy(compositeKey[32:], slotHash[:])

		if err := t.tx.Put(modules.HashedStorage, compositeKey[:], v); err != nil {
			return err
		}
	}

	return nil
}

func (t *TrieRootComputer) deleteAccountStorage(addrHash types.Hash) error {
	// Delete all storage entries for this account from HashedStorage.
	// HashedStorage is DupSort with key prefix = addrHash[32] + incarnation[8].
	// We scan for any key starting with addrHash and delete.
	c, err := t.tx.Cursor(modules.HashedStorage)
	if err != nil {
		return err
	}
	defer c.Close()

	var toDelete [][]byte
	for k, _, err := c.Seek(addrHash[:]); k != nil && len(k) >= 32; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		if !bytes.Equal(k[:32], addrHash[:]) {
			break
		}
		toDelete = append(toDelete, append([]byte{}, k...))
	}
	for _, k := range toDelete {
		if err := t.tx.Delete(modules.HashedStorage, k); err != nil {
			return err
		}
	}

	// Also clean up TrieOfStorage entries for this account.
	tc, err := t.tx.Cursor(modules.TrieOfStorage)
	if err != nil {
		return err
	}
	defer tc.Close()

	toDelete = toDelete[:0]
	for k, _, err := tc.Seek(addrHash[:]); k != nil && len(k) >= 32; k, _, err = tc.Next() {
		if err != nil {
			return err
		}
		if !bytes.Equal(k[:32], addrHash[:]) {
			break
		}
		toDelete = append(toDelete, append([]byte{}, k...))
	}
	for _, k := range toDelete {
		if err := t.tx.Delete(modules.TrieOfStorage, k); err != nil {
			return err
		}
	}

	return nil
}

func (t *TrieRootComputer) keccakAddr(addr types.Address) types.Hash {
	t.hasher.Reset()
	t.hasher.Write(addr[:])
	var h types.Hash
	t.hasher.Sum(h[:0])
	return h
}

func (t *TrieRootComputer) keccakHash(h types.Hash) types.Hash {
	t.hasher.Reset()
	t.hasher.Write(h[:])
	var out types.Hash
	t.hasher.Sum(out[:0])
	return out
}
