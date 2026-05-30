// n42-stateless-transition verifies a REAL block's state transition with NO full
// state trie, from a single hashed-canonical N42 datadir (D:/N42-hashed).
//
// It works in REVERSE: D:/N42-hashed holds only the head (post-state) trie, so
// we take block N's post-state proofs (anchored at header[N].Root) plus block
// N's changeset OLD values (AccountChangeSet/StorageChangeSet store the pre-block
// value of every touched key), revert the touched keys to those old values
// through the P8 stateless updater, and check the recomputed root equals
// header[N-1].Root. That is a complete stateless verification of N-1 -> N (run
// backwards), anchored to two independently-stored canonical headers.
//
// This is P8 ③b Step 3. Scope: the datadir holds ONLY the head trie, so proofs
// exist only at the head post-state — therefore only the HEAD block's transition
// (head-1 -> head) is verifiable here, which is exactly the live/minimal-client
// case (verify each new block as it arrives). Non-head --block is rejected with a
// warning (its post-state trie is not stored). Created keys (accounts/slots)
// revert via delete; the branch-collapse sibling nodes a per-key proof omits are
// supplied by a neighbour-retain WitnessRetainer pass. A SELF-DESTRUCT in the
// head block would have no post-state storage to restore (irreversible from post
// alone) and is flagged; post-EIP-6780 these are essentially absent at tip.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func nopAccHC(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
	return nil
}
func nopStorHC(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
	return nil
}

// touched aggregates one address's per-block changes.
type touched struct {
	addr      types.Address
	oldEnc    []byte // AccountChangeSet old account encoding; nil = not in account CS
	inAcctCS  bool
	slots     map[types.Hash][]byte // plaintext slot -> old (pre-block) value
	slotOrder []types.Hash
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "hashed-canonical N42 chaindata dir")
	blockFlag := flag.Uint64("block", 0, "block number to verify (0 = canonical head)")
	outPath := flag.String("out", "stateless-transition.txt", "result summary file")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginRo(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// Resolve canonical head: this datadir holds ONLY the head trie, so proofs
	// can only be generated against the head post-state. Verifying block N
	// requires the datadir's trie to be at N — i.e. N == head.
	var headNum uint64
	{
		c, _ := tx.Cursor(kv.HeaderCanonical)
		k, _, lerr := c.Last()
		c.Close()
		if lerr != nil || len(k) != 8 {
			fmt.Fprintln(os.Stderr, "cannot find canonical head")
			os.Exit(1)
		}
		headNum = binary.BigEndian.Uint64(k)
	}
	blockN := *blockFlag
	if blockN == 0 {
		blockN = headNum
	}
	if blockN != headNum {
		fmt.Fprintf(os.Stderr, "WARNING: block %d != head %d — this head-only datadir can only\n"+
			"  generate proofs at the head post-state, so verification will fail for non-head\n"+
			"  blocks (their post-state trie is not stored). Run without --block to verify head.\n",
			blockN, headNum)
	}
	postRoot := headerRoot(tx, blockN)
	preRoot := headerRoot(tx, blockN-1)
	if postRoot == nil || preRoot == nil {
		fmt.Fprintf(os.Stderr, "missing header(s): pre(%d)=%v post(%d)=%v\n", blockN-1, preRoot != nil, blockN, postRoot != nil)
		os.Exit(1)
	}

	// 1. Read block N changesets (OLD/pre-block values, plaintext keys).
	tch := map[types.Address]*touched{}
	get := func(a types.Address) *touched {
		t := tch[a]
		if t == nil {
			t = &touched{addr: a, slots: map[types.Hash][]byte{}}
			tch[a] = t
		}
		return t
	}
	var bk [8]byte
	binary.BigEndian.PutUint64(bk[:], blockN)

	// AccountChangeSet: key=blockNum, dup=addr(20)+oldAccEnc.
	if c, cerr := tx.CursorDupSort(kv.AccountChangeSet); cerr == nil {
		for v, e := c.SeekBothRange(bk[:], nil); v != nil; _, v, e = c.NextDup() {
			if e != nil {
				break
			}
			_, addrB, oldEnc, derr := changeset.DecodeAccounts(bk[:], v)
			if derr != nil {
				continue
			}
			t := get(types.BytesToAddress(addrB))
			t.inAcctCS = true
			t.oldEnc = append([]byte(nil), oldEnc...)
		}
		c.Close()
	}
	// StorageChangeSet: key=blockNum+addr, dup=slot(32)+oldValue.
	if c, cerr := tx.CursorDupSort(kv.StorageChangeSet); cerr == nil {
		for k, v, e := c.Seek(bk[:]); k != nil; k, v, e = c.Next() {
			if e != nil || len(k) < 8 || binary.BigEndian.Uint64(k[:8]) != blockN {
				break
			}
			_, ks, oldVal, derr := changeset.DecodeStorage(k, v)
			if derr != nil {
				continue
			}
			// ks = addr(20) + slot(32)
			addr := types.BytesToAddress(ks[:20])
			var slot types.Hash
			copy(slot[:], ks[20:52])
			t := get(addr)
			if _, seen := t.slots[slot]; !seen {
				t.slotOrder = append(t.slotOrder, slot)
			}
			t.slots[slot] = append([]byte(nil), oldVal...)
		}
		c.Close()
	}

	// Deterministic address order.
	addrs := make([]types.Address, 0, len(tch))
	for a := range tch {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return addrs[i].String() < addrs[j].String() })

	// 2. Per touched address: post-state proof at head + reverse change.
	acctNodes := map[string][]byte{}     // dedup account-trie multiproof nodes
	addNodes := func(dst map[string][]byte, ns [][]byte) {
		for _, n := range ns {
			dst[string(keccakB(n))] = append([]byte(nil), n...)
		}
	}
	var changes []stateless.AccountChange
	var nCreated, nSelfdestruct, nStorageOnly, nProofMs int64
	// Deleted keys (created in N) revert via delete; record them so we can add
	// their trie neighbours to the witness (the branch-collapse sibling nodes a
	// single-key proof omits).
	var createdAccts []types.Hash
	type slotKey struct{ a, s types.Hash }
	var createdSlots []slotKey
	t0 := time.Now()

	for _, addr := range addrs {
		t := tch[addr]
		addrHash, _ := types.HashData(addr[:])

		var postAcc account.StateAccount
		postAcc.Reset()
		postExists := false
		if enc, gerr := tx.GetOne(kv.HashedAccounts, addrHash[:]); gerr == nil && len(enc) > 0 {
			_ = postAcc.DecodeForStorage(enc)
			postExists = true
		}

		// Per-account proof at the head root.
		ps := time.Now()
		rl := trie.NewRetainList(0)
		pr, perr := trie.NewProofRetainer(addr, &postAcc, t.slotOrder, rl)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "proof retainer %s: %v\n", addr.Hex(), perr)
			os.Exit(1)
		}
		loader := trie.NewFlatDBTrieLoader("transition", rl, nopAccHC, nopStorHC, false)
		loader.SetProofRetainer(pr)
		if _, lerr := loader.CalcTrieRoot(tx, nil); lerr != nil {
			fmt.Fprintf(os.Stderr, "calc %s: %v\n", addr.Hex(), lerr)
			os.Exit(1)
		}
		res, rerr := pr.ProofResult()
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "proof result %s: %v\n", addr.Hex(), rerr)
			os.Exit(1)
		}
		nProofMs += time.Since(ps).Milliseconds()
		addNodes(acctNodes, res.AccountProof)

		ch := stateless.AccountChange{AddrHash: addrHash}
		switch {
		case t.inAcctCS && len(t.oldEnc) == 0:
			// Created in N -> revert by deleting.
			ch.Deleted = true
			nCreated++
			createdAccts = append(createdAccts, addrHash)
		default:
			var oldAcc account.StateAccount
			if t.inAcctCS {
				_ = oldAcc.DecodeForStorage(t.oldEnc)
			} else {
				oldAcc = postAcc // storage-only: account fields unchanged
				nStorageOnly++
			}
			ch.Nonce = oldAcc.Nonce
			ch.Balance.Set(&oldAcc.Balance)
			chash := oldAcc.CodeHash
			if chash == (types.Hash{}) {
				chash = types.BytesToHash(emptyCodeHash())
			}
			ch.CodeHash = chash[:]

			if len(t.slotOrder) > 0 {
				if !postExists {
					nSelfdestruct++ // post-state has no storage subtree to revert
				}
				ch.StorageRoot = res.StorageHash[:]
				stNodes := map[string][]byte{}
				for i := range res.StorageProof {
					addNodes(stNodes, res.StorageProof[i].Proof)
				}
				for _, n := range stNodes {
					ch.StorageProof = append(ch.StorageProof, n)
				}
				for _, slot := range t.slotOrder {
					slotHash, _ := types.HashData(slot[:])
					ch.Storage = append(ch.Storage, stateless.StorageChange{SlotHash: slotHash, Value: t.slots[slot]})
					if len(t.slots[slot]) == 0 { // slot created in N -> revert deletes it
						createdSlots = append(createdSlots, slotKey{addrHash, slotHash})
					}
				}
			}
		}
		changes = append(changes, ch)
	}

	// Neighbour-retain pass: for every key DELETED by the reverse (created in N),
	// add its trie neighbours' hashed keys so the witness carries the sibling
	// subtree nodes the branch-collapse needs. One CalcTrieRoot over a
	// WitnessRetainer yields a flat node set; merged into the proof it is
	// resolved by hash, so the extra nodes are harmless where unneeded.
	var siblingNodes [][]byte
	nNeighborKeys := 0
	if len(createdAccts) > 0 || len(createdSlots) > 0 {
		rl := trie.NewRetainList(0)
		wr := trie.NewWitnessRetainer(rl)
		for _, ah := range createdAccts {
			for _, nb := range acctNeighbors(tx, ah) {
				wr.AddHashedKey(nb)
				nNeighborKeys++
			}
		}
		for _, ck := range createdSlots {
			for _, nb := range slotNeighbors(tx, ck.a, ck.s) {
				var composite [64]byte
				copy(composite[:32], ck.a[:])
				copy(composite[32:], nb)
				wr.AddHashedKey(composite[:])
				nNeighborKeys++
			}
		}
		loader := trie.NewFlatDBTrieLoader("transition-neighbors", rl, nopAccHC, nopStorHC, false)
		loader.SetWitnessRetainer(wr)
		if _, lerr := loader.CalcTrieRoot(tx, nil); lerr != nil {
			fmt.Fprintln(os.Stderr, "neighbor calc:", lerr)
			os.Exit(1)
		}
		siblingNodes = wr.Nodes()
		for _, n := range siblingNodes {
			acctNodes[string(keccakB(n))] = n
		}
		// Make the sibling nodes available to every storage subtree resolution too.
		for i := range changes {
			if len(changes[i].StorageProof) > 0 {
				changes[i].StorageProof = append(changes[i].StorageProof, siblingNodes...)
			}
		}
	}

	acctProof := make([][]byte, 0, len(acctNodes))
	for _, n := range acctNodes {
		acctProof = append(acctProof, n)
	}
	bp := &stateless.BlockProof{Number: blockN, AccountProof: acctProof, Changes: changes}

	// 3. Reverse-apply old values; recomputed root must equal preRoot.
	verr := stateless.VerifyStateRoot(postRoot, preRoot, bp)

	var b []byte
	add := func(f string, a ...interface{}) { b = append(b, []byte(fmt.Sprintf(f, a...))...) }
	add("=== n42-stateless-transition ===\n")
	add("dir            : %s\n", *dir)
	add("block N        : %d\n", blockN)
	add("preRoot  (N-1) : 0x%x\n", preRoot)
	add("postRoot (N)   : 0x%x\n", postRoot)
	add("touched addrs  : %d\n", len(addrs))
	add("  created      : %d (reverted by delete)\n", nCreated)
	add("  storage-only : %d\n", nStorageOnly)
	add("  selfdestruct?: %d (post storage absent — irreversible)\n", nSelfdestruct)
	add("neighbor retain: %d keys -> %d sibling nodes\n", nNeighborKeys, len(siblingNodes))
	add("acct multiproof: %d nodes\n", len(acctProof))
	add("proof gen total: %dms (per-account passes)\n", nProofMs)
	add("wallclock      : %s\n", time.Since(t0).Truncate(time.Millisecond))
	if verr != nil {
		add("VERIFY (N-1<-N): FAIL: %v\n", verr)
	} else {
		add("VERIFY (N-1<-N): PASS — reverse transition reproduces header[N-1].stateRoot\n")
	}
	add("RESULT         : %s\n", map[bool]string{true: "PASS", false: "FAIL"}[verr == nil])

	if werr := os.WriteFile(*outPath, b, 0o644); werr != nil {
		fmt.Fprintln(os.Stderr, "write out:", werr)
	}
	fmt.Fprint(os.Stderr, string(b))
	if verr != nil {
		os.Exit(2)
	}
}

// acctNeighbors returns the raw 32B hashed keys of addrHash's predecessor and
// successor in the account trie (HashedAccount), whose proofs carry the sibling
// subtree a delete-collapse needs.
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

// slotNeighbors returns the predecessor/successor hashed slot keys (32B) of
// slotHash within addrHash's storage subtree (HashedStorage DupSort).
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

func headerRoot(tx kv.Tx, n uint64) []byte {
	hash, err := rawdb.ReadCanonicalHash(tx, n)
	if err != nil || hash == (types.Hash{}) {
		return nil
	}
	hdr := rawdb.ReadHeader(tx, hash, n)
	if hdr == nil {
		return nil
	}
	r := hdr.Root
	return r[:]
}

func keccakB(b []byte) []byte {
	h, _ := types.HashData(b)
	return h[:]
}

func emptyCodeHash() []byte {
	h, _ := types.HashData(nil)
	return h[:]
}
