package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// appendWriter does SORTED bulk writes into a dst table via cursor Append (the
// fast path: no B-tree search per row, sequential page fill — 3-10× faster than
// Put for sorted input). Works for non-dup AND AutoDupSort tables (plain Append;
// the cursor splits AutoDupSort keys internally, matching etl.Collector.Load).
// It commits every commitEvery rows and reopens the tx+cursor (Append stays valid
// because the source is sorted, so the next key > the committed table's max key).
// newAppendWriter returns the dst table's current last key so the caller can SEEK
// the (sorted) source PAST it and RESUME without re-writing — supporting "续传".
type appendWriter struct {
	db          kv.RwDB
	table       string
	commitEvery uint64
	tx          kv.RwTx
	c           kv.RwCursor
	n           uint64
}

func newAppendWriter(ctx context.Context, db kv.RwDB, table string, commitEvery uint64) (*appendWriter, []byte, error) {
	w := &appendWriter{db: db, table: table, commitEvery: commitEvery}
	var err error
	if w.tx, err = db.BeginRw(ctx); err != nil {
		return nil, nil, err
	}
	if w.c, err = w.tx.RwCursor(table); err != nil {
		return nil, nil, err
	}
	lastK, _, err := w.c.Last()
	if err != nil {
		return nil, nil, err
	}
	return w, append([]byte(nil), lastK...), nil
}

func (w *appendWriter) append(k, v []byte) error {
	if err := w.c.Append(k, v); err != nil {
		return fmt.Errorf("append %s k=%x: %w", w.table, k, err)
	}
	w.n++
	if w.commitEvery > 0 && w.n%w.commitEvery == 0 {
		if err := w.tx.Commit(); err != nil {
			return err
		}
		var err error
		if w.tx, err = w.db.BeginRw(context.Background()); err != nil {
			return err
		}
		if w.c, err = w.tx.RwCursor(w.table); err != nil {
			return err
		}
	}
	return nil
}

// flush commits the final tx. Safe to call once; sets tx nil.
func (w *appendWriter) flush() error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	w.tx = nil
	return err
}

func (w *appendWriter) rollback() {
	if w.tx != nil {
		w.tx.Rollback()
		w.tx = nil
	}
}

// decodeRethBytecode extracts the raw deployed EVM bytecode from reth's
// stored Bytecode Compact value, verified against the codehash key.
//
// Layout: [u32 BE = stored bytecode length L][stored bytecode v[4:4+L]]
// [trailer: original_len + jumptable]. For LegacyRaw/Eip7702/Eof the stored
// bytecode IS the raw code (keccak matches directly). For LegacyAnalyzed the
// stored bytecode is PADDED (original || up-to-33 zero bytes for jumpdest
// analysis); the raw code is a prefix stored[:L-p]. revm's padding amount p
// is code-dependent (observed 1..33), so we find it by matching keccak
// against the codehash key — robust regardless of the exact Compact trailer
// encoding (which is the unvendored reth-codecs 0.4.0 struct codec).
func decodeRethBytecode(codeHash, v []byte) []byte {
	if len(v) < 4 {
		return nil
	}
	L := int(binary.BigEndian.Uint32(v[0:4]))
	if 4+L > len(v) {
		return nil
	}
	stored := v[4 : 4+L]
	var kh types.Hash
	copy(kh[:], codeHash)
	// p=0 (raw) and p=33 (most common analyzed padding) first, then scan.
	if crypto.Keccak256Hash(stored) == kh {
		return stored
	}
	for _, p := range []int{33, 24, 1} {
		if p <= L && crypto.Keccak256Hash(stored[:L-p]) == kh {
			return stored[:L-p]
		}
	}
	for p := 2; p <= 64 && p <= L; p++ {
		if crypto.Keccak256Hash(stored[:L-p]) == kh {
			return stored[:L-p]
		}
	}
	return nil
}

// say prints a monitorable line to stderr (the N42 logger is silent without
// a configured handler; fmt to stderr is guaranteed visible for long runs).
func say(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

// progress logs every 15s.
type prog struct {
	t0, last time.Time
	logger   log.Logger
	name     string
}

func newProg(name string, logger log.Logger) *prog {
	now := time.Now()
	return &prog{t0: now, last: now, logger: logger, name: name}
}
func (p *prog) tick(n uint64) {
	if time.Since(p.last) < 15*time.Second {
		return
	}
	p.last = time.Now()
	rate := float64(n) / time.Since(p.t0).Seconds()
	say("  %s progress: done=%d rate/s=%d elapsed=%s", p.name, n, int(rate),
		time.Since(p.t0).Truncate(time.Second))
}

// migrateHashedAccounts: key copy, value reth-Compact → N42 MarshalV2.
// Append (sorted bulk) + resume (skip already-written) + graceful SIGINT.
func migrateHashedAccounts(ctx context.Context, reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE acc: HashedAccounts (reth Compact -> MarshalV2, Append+resume)")
	p := newProg("acc", logger)
	rtx, err := reth.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.Cursor(rethHashedAccounts)
	if err != nil {
		return err
	}
	defer rc.Close()

	w, lastK, err := newAppendWriter(ctx, dst, n42HashedAccounts, commitEvery)
	if err != nil {
		return err
	}
	defer w.rollback()

	var k, v []byte
	if len(lastK) > 0 {
		for k, v, err = rc.Seek(lastK); k != nil && err == nil && bytes.Compare(k, lastK) <= 0; k, v, err = rc.Next() {
		}
		say("acc resume: dst last=%x", lastK)
	} else {
		k, v, err = rc.First()
	}

	var written, decodeFail, skipEmpty uint64
	for ; k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			continue
		}
		nonce, bal, codeHash, ok := decodeRethAccount(v)
		if !ok {
			decodeFail++
			continue
		}
		if codeHash == (types.Hash{}) {
			codeHash = emptyCodeHash
		}
		if nonce == 0 && bal.IsZero() && codeHash == emptyCodeHash {
			skipEmpty++
			continue
		}
		acc := account.StateAccount{Nonce: nonce, Balance: bal, CodeHash: codeHash}
		if err := w.append(k, acc.MarshalV2()); err != nil {
			return err
		}
		written++
		p.tick(written)
		select {
		case <-ctx.Done():
			if e := w.flush(); e != nil {
				return e
			}
			say("PHASE acc interrupted: committed written=%d (resume next run)", written)
			return ctx.Err()
		default:
		}
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := w.flush(); err != nil {
		return err
	}
	say("PHASE acc done: written=%d decodeFail=%d skipEmpty=%d", written, decodeFail, skipEmpty)
	return nil
}

// migrateHashedStorages: DupSort byte-copy. reth dup-value = 32B slotHash +
// trimmed U256; N42 (post-incarnation) identical. We reconstruct the 64B
// logical key (addrHash + slotHash) and Put via AutoConv, which splits it
// back into the same dup-key/dup-value.
func migrateHashedStorages(ctx context.Context, reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE sto: HashedStorages (AutoDupSort, Append+resume)")
	p := newProg("sto", logger)
	rtx, err := reth.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.CursorDupSort(rethHashedStorages)
	if err != nil {
		return err
	}
	defer rc.Close()

	w, lastK, err := newAppendWriter(ctx, dst, n42HashedStorage, commitEvery)
	if err != nil {
		return err
	}
	defer w.rollback()

	// Resume: dst last key is the composite addrHash(32)+slotHash(32). Seek the
	// source to that addrHash, then skip every (addr,slot) pair whose composite
	// is <= lastK (one account's already-written slots), landing on the first >.
	var k, v []byte
	if len(lastK) >= 64 {
		for k, v, err = rc.Seek(lastK[:32]); k != nil && err == nil; k, v, err = rc.Next() {
			if len(k) != 32 || len(v) < 32 {
				continue
			}
			comp := append(append(make([]byte, 0, 64), k...), v[:32]...)
			if bytes.Compare(comp, lastK) > 0 {
				break
			}
		}
		say("sto resume: dst last=%x", lastK)
	} else {
		k, v, err = rc.First()
	}

	composite := make([]byte, 64)
	var written, shortVal uint64
	for ; k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			continue
		}
		if len(v) < 32 {
			shortVal++
			continue
		}
		slotHash := v[:32]
		val := v[32:]
		if len(val) == 0 {
			continue
		}
		copy(composite[:32], k)
		copy(composite[32:], slotHash)
		if err := w.append(composite, val); err != nil {
			return err
		}
		written++
		p.tick(written)
		select {
		case <-ctx.Done():
			if e := w.flush(); e != nil {
				return e
			}
			say("PHASE sto interrupted: committed written=%d (resume next run)", written)
			return ctx.Err()
		default:
		}
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := w.flush(); err != nil {
		return err
	}
	say("PHASE sto done: written=%d shortVal=%d", written, shortVal)
	return nil
}

// migrateAccountsTrie: full byte-copy of reth's AccountsTrie.
//
// DEFAULT path (re-enabled 2026-05-29, P7-A). reth AccountsTrie nodes are
// byte-compatible with N42's MarshalTrieNode (same state/tree/hash mask order;
// reth omits the "+1" own-hash, which UnmarshalTrieNode detects via
// OnesCount16(hasHash)+1==nHashes and treats as "no cached own-hash" → the
// node's hash is recomputed). reth also omits the keylen-0 global root record,
// but so does N42 (AccTrieCursor walks from First(), never relying on it), so
// the shapes already match. The earlier #150 deprecation was a pre-P6 artifact:
// the on-disk stale in D:/N42-hashed was written by a pre-P6 binary, not caused
// by verbatim import on the current binary. Verified: cmd/n42-reth-eip2935-repro
// (real reth nodes, verbatim incremental == full rebuild across block
// 25,191,537) + cmd/n42-reth-trie-probe (account/storage empty-path shapes).
// The `vtrie` verify phase confirms the imported trie root before head is set;
// the `rtrie` rebuild remains available as a fallback if vtrie ever mismatches.
func migrateAccountsTrie(ctx context.Context, reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE tacc: AccountsTrie -> TrieOfAccounts (byte-copy)")
	p := newProg("tacc", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.Cursor(rethAccountsTrie)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	var written uint64
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		kk := append([]byte(nil), k...)
		vv := append([]byte(nil), v...)
		if err := wtx.Put(n42TrieAccounts, kk, vv); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		select {
		case <-ctx.Done():
			if err := wtx.Commit(); err != nil {
				return err
			}
			wtx = nil
			say("PHASE tacc interrupted: committed written=%d", written)
			return ctx.Err()
		default:
		}
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE tacc done: written=%d", written)
	return nil
}

// migrateStoragesTrie: byte-copy of reth's StoragesTrie (path extracted).
//
// DEFAULT path (re-enabled 2026-05-29, P7-A). reth omits the per-contract
// keylen-32 empty-path storage root, but StorageTrieCursor.SeekToAccount
// synthesizes a virtual lvl-0 root (P6 fix) so the loader walks each storage
// subtree correctly without it. The EIP-2935 storage root that previously
// "wedged" at 25,191,537 was a pre-P6 on-disk artifact, not a verbatim-import
// fault: cmd/n42-reth-eip2935-repro replays the REAL reth EIP-2935 nodes across
// 2755 incremental rounds (through block 25,191,537) and verbatim incremental
// matches full rebuild exactly. `vtrie` verifies the whole-state root post-
// import; `rtrie` rebuild stays as a fallback.
func migrateStoragesTrie(ctx context.Context, reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	say("PHASE tsto: StoragesTrie -> TrieOfStorage (extract nibble path)")
	p := newProg("tsto", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.CursorDupSort(rethStoragesTrie)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	var written, badSub uint64
	keyBuf := make([]byte, 0, 32+64)
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			continue
		}
		if len(v) < 65 { // 65B subkey + node
			badSub++
			continue
		}
		sub := v[:65]
		node := v[65:]
		pathLen := int(sub[64])
		if pathLen > 64 {
			badSub++
			continue
		}
		path := sub[:pathLen] // nibble path, 1 nibble/byte
		keyBuf = append(keyBuf[:0], k...)
		keyBuf = append(keyBuf, path...)
		kk := append([]byte(nil), keyBuf...)
		vv := append([]byte(nil), node...)
		if err := wtx.Put(n42TrieStorage, kk, vv); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		select {
		case <-ctx.Done():
			if err := wtx.Commit(); err != nil {
				return err
			}
			wtx = nil
			say("PHASE tsto interrupted: committed written=%d", written)
			return ctx.Err()
		default:
		}
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE tsto done: written=%d badSub=%d", written, badSub)
	return nil
}

// migrateBytecodes: key copy, strip reth 1B BytecodeType prefix (0=Raw).
func migrateBytecodes(ctx context.Context, reth kv.RoDB, dst kv.RwDB, commitEvery, limit uint64, logger log.Logger) error {
	_ = ctx // code phase is small; runs to completion (re-run on resume is cheap)
	say("PHASE code: Bytecodes -> Code (strip 1B type prefix)")
	p := newProg("code", logger)
	rtx, err := reth.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer rtx.Rollback()
	rc, err := rtx.Cursor(rethBytecodes)
	if err != nil {
		return err
	}
	defer rc.Close()

	wtx, err := dst.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer func() {
		if wtx != nil {
			wtx.Rollback()
		}
	}()

	// Clear stale Code first (the earlier 1-byte-strip migration was wrong).
	if err := wtx.ClearBucket(n42Code); err != nil {
		return fmt.Errorf("clear Code: %w", err)
	}

	var written, skipped, decodeFail uint64
	for k, v, err := rc.First(); k != nil; k, v, err = rc.Next() {
		if err != nil {
			return fmt.Errorf("reth iter: %w", err)
		}
		if len(k) != 32 {
			skipped++
			continue
		}
		raw := decodeRethBytecode(k, v)
		if raw == nil {
			decodeFail++
			continue
		}
		kk := append([]byte(nil), k...)
		code := append([]byte(nil), raw...)
		if err := wtx.Put(n42Code, kk, code); err != nil {
			return fmt.Errorf("put: %w", err)
		}
		written++
		if written%commitEvery == 0 {
			if err := wtx.Commit(); err != nil {
				return err
			}
			if wtx, err = dst.BeginRw(context.Background()); err != nil {
				return err
			}
		}
		p.tick(written)
		if limit > 0 && written >= limit {
			break
		}
	}
	if err := wtx.Commit(); err != nil {
		return err
	}
	wtx = nil
	say("PHASE code done: written=%d skipped=%d decodeFail=%d", written, skipped, decodeFail)
	return nil
}

// rebuildTrieFromLeaves rebuilds TrieAccount + TrieStorage from the already-
// migrated HashedAccount + HashedStorage leaves, producing N42-native,
// self-consistent BranchNodeCompact records (including the own-hash/rootHash
// field). This is the fix for #150: copying reth's trie nodes verbatim (the
// deprecated tacc/tsto phases) left stale rootHash fields that wedged sync.
//
// It mirrors cmd/n42-rebuild-trie's verify-before-clear flow:
//  1. full RetainList(1<<30) → Retain() true everywhere → the loader IGNORES
//     any existing TrieOf* (even reth-copied nodes) and descends to leaves,
//     streaming freshly-computed nodes into ETL collectors;
//  2. verify the recomputed root against expectRoot (the head block's mainnet
//     stateRoot) BEFORE touching the destination trie tables — a mismatch
//     aborts with TrieOf* untouched;
//  3. only after the check passes, clear + bulk-load TrieAccount/TrieStorage.
//
// expectRoot may be "" to skip the check (not recommended; you then have no
// guarantee the migrated leaves reproduce the expected state root).
func rebuildTrieFromLeaves(dst kv.RwDB, expectRoot, tmpdir string, accBufGB, stoBufGB uint64, logger log.Logger) error {
	say("PHASE rtrie: rebuild TrieAccount/TrieStorage from hashed leaves (verify-before-clear, #150 fix)")
	if err := os.MkdirAll(tmpdir, 0o755); err != nil {
		return fmt.Errorf("mkdir tmpdir: %w", err)
	}
	ctx := context.Background()
	t0 := time.Now()

	accColl := etl.NewCollector("migrate-rtrie-acc", tmpdir,
		etl.NewSortableBuffer(datasize.ByteSize(accBufGB)*datasize.GB), logger)
	defer accColl.Close()
	stoColl := etl.NewCollector("migrate-rtrie-sto", tmpdir,
		etl.NewSortableBuffer(datasize.ByteSize(stoBufGB)*datasize.GB), logger)
	defer stoColl.Close()

	var accN, stoN uint64
	accCollector := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 || hasState == 0 {
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		accN++
		return accColl.Collect(append([]byte(nil), keyHex...), append([]byte(nil), v...))
	}
	storCollector := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append(make([]byte, 0, len(accWithInc)+len(keyHex)), accWithInc...), keyHex...)
		if len(k) == 0 || hasState == 0 {
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		stoN++
		return stoColl.Collect(k, append([]byte(nil), v...))
	}

	say("  rtrie: CalcTrieRoot full descent from leaves (full RetainList ignores any existing TrieOf*)")
	var root [32]byte
	{
		rtx, err := dst.BeginRo(ctx)
		if err != nil {
			return err
		}
		loader := trie.NewFlatDBTrieLoader("migrate-rtrie", trie.NewRetainList(1<<30), accCollector, storCollector, false)
		r, err := loader.CalcTrieRoot(rtx, nil)
		rtx.Rollback()
		if err != nil {
			return fmt.Errorf("CalcTrieRoot: %w", err)
		}
		root = r
	}
	say("  rtrie computed: root=%x accNodes=%d stoNodes=%d elapsed=%s",
		root[:], accN, stoN, time.Since(t0).Truncate(time.Second))

	if expectRoot != "" && fmt.Sprintf("0x%x", root[:]) != expectRoot {
		return fmt.Errorf("rtrie ROOT MISMATCH: got 0x%x want %s — NOT persisting (TrieOf* untouched)", root[:], expectRoot)
	}
	if expectRoot == "" {
		say("  rtrie WARNING: --expect-root empty, skipping verify (cannot confirm leaves reproduce expected state root)")
	}

	// Verified (or skip) → safe to clear + load. Two txns, matching
	// cmd/n42-rebuild-trie, to bound per-txn dirty pages.
	say("  rtrie: clearing TrieAccount + TrieStorage")
	if err := dst.Update(ctx, func(tx kv.RwTx) error {
		if err := tx.ClearBucket(n42TrieAccounts); err != nil {
			return err
		}
		return tx.ClearBucket(n42TrieStorage)
	}); err != nil {
		return fmt.Errorf("clear: %w", err)
	}
	say("  rtrie: loading TrieAccount + TrieStorage")
	if err := dst.Update(ctx, func(tx kv.RwTx) error {
		if err := accColl.Load(tx, n42TrieAccounts, etl.IdentityLoadFunc, etl.TransformArgs{}); err != nil {
			return fmt.Errorf("load TrieAccount: %w", err)
		}
		return stoColl.Load(tx, n42TrieStorage, etl.IdentityLoadFunc, etl.TransformArgs{})
	}); err != nil {
		return fmt.Errorf("load: %w", err)
	}
	say("PHASE rtrie done: root=0x%x accNodes=%d stoNodes=%d total=%s",
		root[:], accN, stoN, time.Since(t0).Truncate(time.Second))
	return nil
}

// verifyVerbatimTrie confirms the verbatim-imported TrieAccount/TrieStorage
// reproduce the expected head stateRoot WITHOUT rebuilding. It runs one
// incremental ComputeRoot with an EMPTY dirty set: the loader reads every
// cached (verbatim reth) node and, for storage subtrees missing reth's
// keylen-32 root, synthesizes the lvl-0 root via the P6 cursor path. The
// resulting whole-state root is compared to --expect-root. The check is
// read-only (the txn is rolled back), so it never mutates the imported trie.
//
// This is the verbatim counterpart to rtrie's verify-before-clear, but far
// cheaper: it reads cached intermediate hashes instead of descending to every
// hashed leaf. A mismatch means the verbatim import is not self-consistent for
// this binary — re-run with `--phases acc,sto,code,rtrie,head` to rebuild.
func verifyVerbatimTrie(dst kv.RwDB, expectRoot string, logger log.Logger) error {
	say("PHASE vtrie: verify verbatim trie root via cached read (no rebuild)")
	if expectRoot == "" {
		say("  vtrie WARNING: --expect-root empty, skipping verify (cannot confirm verbatim trie reproduces the head stateRoot)")
		return nil
	}
	ctx := context.Background()
	t0 := time.Now()
	tx, err := dst.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // read-only verify: never persist

	trc := commitment.NewTrieRootComputer()
	trc.SetIncremental(true)
	trc.SetRwTx(tx)
	root, err := trc.ComputeRoot(nil, nil) // empty dirty → root from all cached verbatim nodes
	if err != nil {
		return fmt.Errorf("vtrie ComputeRoot: %w", err)
	}
	got := fmt.Sprintf("0x%x", root[:])
	if got != expectRoot {
		return fmt.Errorf("vtrie ROOT MISMATCH: got %s want %s — verbatim trie not self-consistent on this binary; "+
			"re-run `--phases acc,sto,code,rtrie,head --expect-root=%s` to rebuild from leaves", got, expectRoot, expectRoot)
	}
	say("PHASE vtrie OK: verbatim trie root %s == expect (verify took %s)", got, time.Since(t0).Truncate(time.Second))
	return nil
}
