// n42-stateless-blockproof-produce builds FORWARD single-block transition proofs
// (layer ③) on real data: it rebuilds state from genesis by applying the V2
// forward changesets per block, and at every K-th block emits a stateless.BlockProof
// for that block — a pre-state account multiproof (+ per-account storage proofs)
// over the block's touched keys, plus the block's NEW-value changeset. Each proof
// is SELF-VERIFIED with stateless.VerifyStateRoot(header[N-1].Root → header[N].Root)
// before it is written, so a served anchor lets a minimal client INDEPENDENTLY
// recompute the state root from the changeset (not merely trust header.Root).
//
// Output: <out>/blockproof-<N>.bin = EncodeBlockProof, consumable by the serve
// /anchor endpoint and verified client-side by MinimalClient.VerifyAgainstChain.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func nopAccHC([]byte, uint16, uint16, uint16, []byte, []byte) error          { return nil }
func nopStorHC([]byte, []byte, uint16, uint16, uint16, []byte, []byte) error { return nil }
func keccakB(b []byte) []byte                                                { h, _ := types.HashData(b); return h[:] }
func emptyCodeHash() []byte                                                  { h, _ := types.HashData(nil); return h[:] }
func emptyStorageRoot() []byte                                               { return keccakB([]byte{0x80}) } // keccak(rlp("")) = empty MPT root

// bppProgressKey holds the last fully-committed block in the trie DB's DbInfo
// bucket, written atomically with the trie state so --resume can continue exactly.
var bppProgressKey = []byte("bpp_progress")

func be8(n uint64) []byte { var b [8]byte; binary.BigEndian.PutUint64(b[:], n); return b[:] }

func main() {
	csDir := flag.String("cs", `D:/N42-eth1177/chain/freezer`, "freezer dir with acctcs/storcs (V2 forward changesets)")
	hdrDir := flag.String("headers", `D:/n42-eth1/chain/freezer`, "columnar headerc dir")
	tmp := flag.String("tmp", filepath.Join(os.TempDir(), "n42-bp-trie"), "temp writable trie datadir (recreated)")
	out := flag.String("out", "", "anchorc freezer dir (default <tmp>/anchorfz)")
	endBlock := flag.Uint64("end", 20000, "build genesis..end (exclusive)")
	kHist := flag.Uint64("k-historical", 10000, "anchor cadence for blocks < --recent-from")
	kRecent := flag.Uint64("k-recent", 1000, "anchor cadence for blocks >= --recent-from")
	recentFrom := flag.Uint64("recent-from", 0, "block at/after which the fine (k-recent) cadence applies; 0 = use k-recent throughout")
	mapGB := flag.Int("mapsize-gb", 64, "temp trie MDBX mapsize GB")
	resume := flag.Bool("resume", false, "continue an interrupted run: keep the existing --tmp trie + --out anchorc, resume from the last committed block (DbInfo/bpp_progress), reconciling the anchorc/sidecar to that block")
	flag.Parse()

	ctx := context.Background()
	logger := log.New()
	if *kHist == 0 || *kRecent == 0 {
		fmt.Fprintln(os.Stderr, "--k-historical and --k-recent must be > 0")
		os.Exit(1)
	}
	if *out == "" {
		*out = filepath.Join(*tmp, "anchorfz")
	}
	if !*resume {
		_ = os.RemoveAll(*tmp) // fresh run: discard any prior trie
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	db, err := mdbx.NewMDBX(logger).Path(filepath.Join(*tmp, "chaindata")).Label(kv.ChainDB).
		PageSize(4096).MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).Open(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open temp db:", err)
		os.Exit(1)
	}
	defer db.Close()

	// Resume: read the last fully-committed block (atomic with the trie state).
	// startBlock is the first block to (re)process; the trie is already at
	// startBlock-1, so ComputeRoot runs incrementally from there.
	var startBlock uint64
	if *resume {
		_ = db.View(ctx, func(rtx kv.Tx) error {
			if v, _ := rtx.GetOne(kv.DatabaseInfo, bppProgressKey); len(v) == 8 {
				startBlock = binary.BigEndian.Uint64(v) + 1
			}
			return nil
		})
		if startBlock > 0 {
			fmt.Fprintf(os.Stderr, "RESUME: trie committed through block %d → continue at %d\n", startBlock-1, startBlock)
		} else {
			fmt.Fprintln(os.Stderr, "RESUME requested but no DbInfo/bpp_progress found → starting fresh")
		}
	}

	hc, err := ethel.OpenHeaderCompact(*hdrDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open headerc:", err)
		os.Exit(1)
	}
	defer hc.Close()

	acctTbl, err := freezer.NewFreezerTableReadOnly(*csDir, "acctcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)
	acctTbl.SetCompressed(true)
	stoTbl, err := freezer.NewFreezerTableReadOnly(*csDir, "storcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer stoTbl.Close()
	stoTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.SetCompressed(true)

	// anchorc fdb (zstd) + anchorc.blocks sidecar (8-byte BE block number per item).
	// Items are sequential = only SUCCESSFUL anchors; the sidecar maps item↔block so
	// any/variable cadence works (the serve binary-searches block→item). Skipped
	// anchors are simply absent (client gets not-found for that block).
	anchorTbl, err := freezer.NewFreezerTableCompressed(*out, "anchorc", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open anchorc freezer:", err)
		os.Exit(1)
	}
	defer anchorTbl.Close()
	sidecarPath := filepath.Join(*out, "anchorc.blocks")

	// Reconcile the freezer (non-transactional) with the committed trie progress:
	// keep only anchors whose block ≤ startBlock-1, dropping any produced after the
	// last trie commit (a hard kill can leave the freezer ahead of the commit). On a
	// fresh run, clear any stale --out anchorc entirely.
	var sidecarBlocks []uint64
	if startBlock > 0 {
		if sb, e := os.ReadFile(sidecarPath); e == nil {
			for i := 0; i+8 <= len(sb); i += 8 {
				sidecarBlocks = append(sidecarBlocks, binary.BigEndian.Uint64(sb[i:i+8]))
			}
		}
	}
	keep := uint64(0)
	for _, b := range sidecarBlocks {
		if b <= startBlock-1 {
			keep++
		} else {
			break // sidecar is ascending; the rest are post-commit
		}
	}
	if startBlock == 0 { // fresh: clear stale anchorc + sidecar
		if err := anchorTbl.TruncateHead(0); err != nil {
			fmt.Fprintln(os.Stderr, "truncate anchorc:", err)
			os.Exit(1)
		}
	} else if keep < anchorTbl.Items() {
		if err := anchorTbl.TruncateHead(keep); err != nil {
			fmt.Fprintln(os.Stderr, "reconcile anchorc TruncateHead:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "RESUME: reconciled anchorc to %d items (≤ block %d)\n", keep, startBlock-1)
	}
	nextItem := keep
	// Open the sidecar truncated to the kept items, positioned for append.
	sidecar, err := os.OpenFile(sidecarPath, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open sidecar:", err)
		os.Exit(1)
	}
	defer sidecar.Close()
	if err := sidecar.Truncate(int64(keep) * 8); err != nil {
		fmt.Fprintln(os.Stderr, "truncate sidecar:", err)
		os.Exit(1)
	}
	if _, err := sidecar.Seek(int64(keep)*8, io.SeekStart); err != nil {
		fmt.Fprintln(os.Stderr, "seek sidecar:", err)
		os.Exit(1)
	}

	tx, err := db.BeginRw(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin rw:", err)
		os.Exit(1)
	}
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(startBlock > 0) // resuming a non-empty trie → incremental from the start

	t0 := time.Now()
	emitted, verifiedOK := 0, 0

	anchorK := func(n uint64) uint64 { // variable cadence: coarse historical, fine recent
		if *recentFrom > 0 && n >= *recentFrom {
			return *kRecent
		}
		return *kHist
	}

	// Path C ("batch root"): non-anchor blocks only ACCUMULATE their forward
	// changeset (last-write-wins per key, in block order) into netA/netS — no
	// CalcTrieRoot. At each anchor block n the window's net is applied with ONE
	// incremental ComputeRoot to bring the trie to n-1 (verified against
	// header[n-1].Root — a strong window-level state-root check), the window is
	// reset, the single-block transition proof for n is captured at n-1, then
	// block n is applied alone (verified against header[n].Root). This computes the
	// root ~2× per anchor instead of once per block and dedups keys touched
	// repeatedly within a window (a hot contract slot is hashed once, not per
	// block). The per-block root is no longer checked; the per-anchor pre/post root
	// verifications + the anchor self-verify (VerifyStateRoot) cover correctness.
	netA := map[types.Address]*account.StateAccount{}
	netS := map[types.Address]map[types.Hash]*uint256.Int{}
	mergeCS := func(dA map[types.Address]*account.StateAccount, dS map[types.Address]map[types.Hash]*uint256.Int) {
		for a, v := range dA {
			netA[a] = v // last write wins (nil = deleted; per-block changesets are faithful deltas)
		}
		for a, slots := range dS {
			m := netS[a]
			if m == nil {
				m = map[types.Hash]*uint256.Int{}
				netS[a] = m
			}
			for s, v := range slots {
				m[s] = v
			}
		}
	}
	firstCompute := startBlock == 0 // a fresh trie needs one bootstrap (incremental=false) pass

	for n := startBlock; n < *endBlock; n++ {
		dA, dS := readBlockChangeset(acctTbl, stoTbl, n)
		if n == 0 || n%anchorK(n) != 0 { // non-anchor (incl. genesis block 0): accumulate only
			mergeCS(dA, dS)
			continue
		}

		// (a) Bring the trie to n-1 by applying the accumulated window net once.
		var rootNm1 types.Hash
		if len(netA) > 0 || len(netS) > 0 {
			trc.SetIncremental(!firstCompute)
			r, cerr := trc.ComputeRoot(netA, netS)
			if cerr != nil {
				fmt.Fprintf(os.Stderr, "ComputeRoot window→%d: %v\n", n-1, cerr)
				os.Exit(1)
			}
			firstCompute = false
			rootNm1 = r
			netA = map[types.Address]*account.StateAccount{}
			netS = map[types.Address]map[types.Hash]*uint256.Int{}
		} else if h, herr := hc.ReadHeader(n - 1); herr == nil { // empty window (resumed at an anchor): trie already at n-1
			rootNm1 = h.Root
		}
		if hnm1, herr := hc.ReadHeader(n - 1); herr == nil && rootNm1 != hnm1.Root {
			fmt.Fprintf(os.Stderr, "ROOT MISMATCH window→block %d: computed %s header %s\n", n-1, rootNm1.Hex(), hnm1.Root.Hex())
			os.Exit(2)
		}

		// (b) Single-block transition proof for n (trie is at n-1).
		bp, bpErr := buildBlockProof(tx, n, dA, dS)

		// (c) Apply block n alone → post-state root.
		trc.SetIncremental(!firstCompute)
		rootN, cerr := trc.ComputeRoot(dA, dS)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "ComputeRoot %d: %v\n", n, cerr)
			os.Exit(1)
		}
		firstCompute = false
		if hn, herr := hc.ReadHeader(n); herr != nil {
			fmt.Fprintf(os.Stderr, "read header %d: %v\n", n, herr)
			os.Exit(1)
		} else if rootN != hn.Root {
			fmt.Fprintf(os.Stderr, "ROOT MISMATCH block %d: computed %s header %s\n", n, rootN.Hex(), hn.Root.Hex())
			os.Exit(2)
		}

		// (d) Self-verify the single-block transition proof and append.
		emitted++
		if bpErr != nil {
			fmt.Fprintf(os.Stderr, "BLOCKPROOF n=%d SKIP (proof build): %v\n", n, bpErr)
		} else if verr := stateless.VerifyStateRoot(rootNm1[:], rootN[:], bp); verr != nil {
			fmt.Fprintf(os.Stderr, "BLOCKPROOF n=%d ✗ FAILED: %v\n", n, verr)
		} else {
			verifiedOK++
			wire := stateless.EncodeBlockProof(bp)
			if werr := anchorTbl.Append(nextItem, wire); werr != nil {
				fmt.Fprintf(os.Stderr, "append anchor %d (item %d): %v\n", n, nextItem, werr)
				os.Exit(1)
			}
			if _, werr := sidecar.Write(be8(n)); werr != nil { // item nextItem → block n
				fmt.Fprintf(os.Stderr, "sidecar write %d: %v\n", n, werr)
				os.Exit(1)
			}
			nextItem++
			fmt.Fprintf(os.Stderr, "BLOCKPROOF n=%d item=%d changes=%d acctNodes=%d pre=%s→post=%s ✓ VERIFIED (%s)\n",
				n, nextItem-1, len(bp.Changes), len(bp.AccountProof), rootNm1.Hex()[:10], rootN.Hex()[:10],
				time.Since(t0).Truncate(time.Second))
		}

		// (e) Commit: the trie is durable at block n. Flush the anchorc + sidecar
		// first, then record progress=n ATOMICALLY with the trie commit so a
		// --resume continues from exactly here (and reconciles the freezer to n).
		if serr := anchorTbl.Sync(); serr != nil {
			fmt.Fprintf(os.Stderr, "anchorc sync %d: %v\n", n, serr)
			os.Exit(1)
		}
		_ = sidecar.Sync()
		if perr := tx.Put(kv.DatabaseInfo, bppProgressKey, be8(n)); perr != nil {
			fmt.Fprintf(os.Stderr, "put progress %d: %v\n", n, perr)
			os.Exit(1)
		}
		if cerr := tx.Commit(); cerr != nil {
			fmt.Fprintf(os.Stderr, "commit %d: %v\n", n, cerr)
			os.Exit(1)
		}
		if tx, err = db.BeginRw(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "re-begin:", err)
			os.Exit(1)
		}
		trc.SetRwTx(tx)
	}
	tx.Rollback() // discard the empty re-begun tx; progress was committed at the last anchor
	if serr := anchorTbl.Sync(); serr != nil {
		fmt.Fprintln(os.Stderr, "anchorc sync:", serr)
		os.Exit(1)
	}
	_ = sidecar.Sync()
	fmt.Fprintf(os.Stderr, "DONE: %d anchors (%d/%d self-VERIFIED) → %s (anchorc fdb + .blocks sidecar; cadence K=%d hist / %d recent from %d) in %s\n",
		nextItem, verifiedOK, emitted, *out, *kHist, *kRecent, *recentFrom, time.Since(t0).Truncate(time.Second))
}

// readBlockChangeset decodes block n's V2 forward changeset (NEW values).
func readBlockChangeset(acctTbl, stoTbl *freezer.FreezerTable, n uint64) (map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) {
	dA := map[types.Address]*account.StateAccount{}
	dS := map[types.Address]map[types.Hash]*uint256.Int{}
	if d, e := acctTbl.Retrieve(n); e == nil && len(d) > 0 {
		es, _ := ethel.DecodeAccountChanges(d)
		for _, ch := range es {
			if len(ch.NewValue) == 0 {
				dA[ch.Address] = nil // deleted
				continue
			}
			a := new(account.StateAccount)
			if a.DecodeForStorage(ch.NewValue) == nil {
				dA[ch.Address] = a
			}
		}
	}
	if d, e := stoTbl.Retrieve(n); e == nil && len(d) > 0 {
		es, _ := ethel.DecodeStorageChanges(d)
		for _, ch := range es {
			if len(ch.CompositeKey) < 52 {
				continue
			}
			var addr types.Address
			copy(addr[:], ch.CompositeKey[:20])
			var slot types.Hash
			copy(slot[:], ch.CompositeKey[20:52])
			if dS[addr] == nil {
				dS[addr] = map[types.Hash]*uint256.Int{}
			}
			v := new(uint256.Int)
			if len(ch.NewValue) > 0 {
				v.SetBytes(ch.NewValue)
			}
			dS[addr][slot] = v
		}
	}
	return dA, dS
}

// buildBlockProof assembles a single-block FORWARD transition BlockProof for block
// n at the PRE-state trie (tx is at header[n-1]): ONE merged account+storage
// multiproof over all touched keys, plus the block's NEW-value changeset. Deleted
// keys get neighbour subtrees retained (forward delete collapses a branch).
//
// All touched keys (account hashes + storage composite keys + delete neighbours)
// are retained in a SINGLE WitnessRetainer and proven with a SINGLE CalcTrieRoot
// walk — not one walk per account. At a dense anchor (hundreds of touched
// accounts in a 25M-block state) the per-account walk was O(accounts × trie scan)
// and dominated runtime; one merged walk is O(trie scan). The flat node set is
// shipped once as AccountProof; the consumer's StateRootUpdater treats it as a
// shared pool for every account's storage subtree, so per-account StorageProof is
// left empty (no node duplication). Each account's pre-state storageRoot is read
// back out of the merged proof via stateless.StorageRootsFromProof.
func buildBlockProof(tx kv.Tx, n uint64, dA map[types.Address]*account.StateAccount, dS map[types.Address]map[types.Hash]*uint256.Int) (*stateless.BlockProof, error) {
	// Union of touched addresses (account-changed ∪ storage-changed), stable order.
	addrs := make([]types.Address, 0, len(dA)+len(dS))
	seen := map[types.Address]bool{}
	add := func(a types.Address) {
		if !seen[a] {
			seen[a] = true
			addrs = append(addrs, a)
		}
	}
	for a := range dA {
		add(a)
	}
	for a := range dS {
		add(a)
	}

	type acctMeta struct {
		addr             types.Address
		addrHash         types.Hash
		preAcc           account.StateAccount
		preStorageExists bool
		allSlots         []types.Hash
	}
	metas := make([]acctMeta, 0, len(addrs))

	rl := trie.NewRetainList(0)
	wr := trie.NewWitnessRetainer(rl)

	for _, addr := range addrs {
		addrHash, _ := types.HashData(addr[:])
		var preAcc account.StateAccount
		preAcc.Reset()
		preExists := false
		if enc, e := tx.GetOne(kv.HashedAccounts, addrHash[:]); e == nil && len(enc) > 0 {
			_ = preAcc.DecodeForStorage(enc)
			preExists = true
		}
		var allSlots []types.Hash
		for s := range dS[addr] {
			allSlots = append(allSlots, s)
		}
		// Storage is proven only when the account has a NON-EMPTY pre-state storage
		// subtree. A created account, or an existing EOA/empty-storage account gaining
		// its first slots, has no pre-subtree: its slots are pure inserts into an
		// empty root (StorageRoot=EmptyRoot, no storage proof). Detect via HashedStorage.
		preStorageExists := false
		if preExists {
			if sc, e := tx.CursorDupSort(kv.HashedStorage); e == nil {
				if v, e2 := sc.SeekBothRange(addrHash[:], make([]byte, 32)); e2 == nil && len(v) >= 32 {
					preStorageExists = true
				}
				sc.Close()
			}
		}

		// Retain the account key (inclusion + storageRoot read-back).
		wr.AddHashedKey(addrHash[:])
		if preStorageExists {
			for _, slot := range allSlots {
				slotHash, _ := types.HashData(slot[:])
				var comp [64]byte
				copy(comp[:32], addrHash[:])
				copy(comp[32:], slotHash[:])
				wr.AddHashedKey(comp[:]) // storage subtree path
			}
		}
		// Deleted account → retain neighbours so a branch collapse has its siblings.
		if newAcc, inAcct := dA[addr]; inAcct && newAcc == nil {
			for _, nb := range acctNeighbors(tx, addrHash) {
				wr.AddHashedKey(nb)
			}
		}
		// Forward-deleted slots (zero value in an existing subtree) → slot neighbours.
		if preStorageExists {
			for _, slot := range allSlots {
				if v := dS[addr][slot]; v == nil || v.IsZero() {
					slotHash, _ := types.HashData(slot[:])
					for _, nb := range slotNeighbors(tx, addrHash, slotHash) {
						var comp [64]byte
						copy(comp[:32], addrHash[:])
						copy(comp[32:], nb)
						wr.AddHashedKey(comp[:])
					}
				}
			}
		}
		metas = append(metas, acctMeta{addr, addrHash, preAcc, preStorageExists, allSlots})
	}

	// ONE walk → merged account+storage multiproof.
	loader := trie.NewFlatDBTrieLoader("bp", rl, nopAccHC, nopStorHC, false)
	loader.SetWitnessRetainer(wr)
	root, lerr := loader.CalcTrieRoot(tx, nil)
	if lerr != nil {
		return nil, fmt.Errorf("calc: %w", lerr)
	}
	allNodes := wr.Nodes()

	// Read each existing-subtree account's pre-state storageRoot out of the proof.
	var withStorage []types.Hash
	for _, m := range metas {
		if m.preStorageExists {
			withStorage = append(withStorage, m.addrHash)
		}
	}
	storageRoots, srerr := stateless.StorageRootsFromProof(root[:], allNodes, withStorage)
	if srerr != nil {
		return nil, fmt.Errorf("storage roots: %w", srerr)
	}

	changes := make([]stateless.AccountChange, 0, len(metas))
	for i := range metas {
		m := &metas[i]
		ch := stateless.AccountChange{AddrHash: m.addrHash}
		newAcc, inAcct := dA[m.addr]
		if inAcct && newAcc == nil {
			ch.Deleted = true
			changes = append(changes, ch)
			continue
		}
		if newAcc != nil { // account fields changed → NEW values
			ch.Nonce = newAcc.Nonce
			ch.Balance.Set(&newAcc.Balance)
			chash := newAcc.CodeHash
			if chash == (types.Hash{}) {
				chash = types.BytesToHash(emptyCodeHash())
			}
			ch.CodeHash = chash[:]
		} else { // storage-only: account fields unchanged (use pre values)
			ch.Nonce = m.preAcc.Nonce
			ch.Balance.Set(&m.preAcc.Balance)
			chash := m.preAcc.CodeHash
			if chash == (types.Hash{}) {
				chash = types.BytesToHash(emptyCodeHash())
			}
			ch.CodeHash = chash[:]
		}
		if len(m.allSlots) > 0 {
			if m.preStorageExists {
				ch.StorageRoot = storageRoots[m.addrHash] // from the merged proof's leaf
			} else {
				ch.StorageRoot = emptyStorageRoot() // created/empty-storage: empty pre-subtree
			}
			// StorageProof left nil: the storage nodes live in the shared AccountProof
			// pool (the consumer's AddStorageProof consults it).
			for _, slot := range m.allSlots {
				slotHash, _ := types.HashData(slot[:])
				val := dS[m.addr][slot]
				var vb []byte
				if val != nil && !val.IsZero() {
					b := val.Bytes32()
					vb = bytes.TrimLeft(b[:], "\x00")
				}
				ch.Storage = append(ch.Storage, stateless.StorageChange{SlotHash: slotHash, Value: vb})
			}
		}
		changes = append(changes, ch)
	}

	return &stateless.BlockProof{Number: n, AccountProof: allNodes, Changes: changes}, nil
}

// acctNeighbors / slotNeighbors return the predecessor/successor hashed keys whose
// proofs carry the sibling subtree a delete-collapse needs.
func acctNeighbors(tx kv.Tx, addrHash types.Hash) [][]byte {
	c, err := tx.Cursor(kv.HashedAccounts)
	if err != nil {
		return nil
	}
	defer c.Close()
	var out [][]byte
	if k, _, e := c.Seek(addrHash[:]); e == nil && k != nil {
		if bytes.Equal(k, addrHash[:]) {
			k, _, e = c.Next()
		}
		if e == nil && len(k) == 32 {
			out = append(out, append([]byte(nil), k...))
		}
	}
	if k, _, e := c.Seek(addrHash[:]); e == nil && k != nil {
		if pk, _, pe := c.Prev(); pe == nil && len(pk) == 32 && !bytes.Equal(pk, addrHash[:]) {
			out = append(out, append([]byte(nil), pk...))
		}
	}
	return out
}

func slotNeighbors(tx kv.Tx, addrHash, slotHash types.Hash) [][]byte {
	c, err := tx.CursorDupSort(kv.HashedStorage)
	if err != nil {
		return nil
	}
	defer c.Close()
	var out [][]byte
	if v, e := c.SeekBothRange(addrHash[:], slotHash[:]); e == nil && v != nil {
		if len(v) >= 32 && bytes.Equal(v[:32], slotHash[:]) {
			if _, v2, e2 := c.NextDup(); e2 == nil && len(v2) >= 32 {
				out = append(out, append([]byte(nil), v2[:32]...))
			}
		} else if len(v) >= 32 {
			out = append(out, append([]byte(nil), v[:32]...))
		}
	}
	if v, e := c.SeekBothRange(addrHash[:], slotHash[:]); e == nil && v != nil {
		if _, pv, pe := c.PrevDup(); pe == nil && len(pv) >= 32 && !bytes.Equal(pv[:32], slotHash[:]) {
			out = append(out, append([]byte(nil), pv[:32]...))
		}
	}
	return out
}
