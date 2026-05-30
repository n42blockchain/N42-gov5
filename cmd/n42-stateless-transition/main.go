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
// This is P8 ③b Step 3. Limits (reported, not hidden): an account SELF-DESTRUCTed
// in block N has no post-state storage to restore (irreversible from post alone);
// such blocks are flagged. Accounts CREATED in N revert via delete, which needs
// the sibling nodes a per-key proof may omit — surfaced as a missing-node error.
package main

import (
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

	// Resolve block N (default canonical head) and the pre/post header roots.
	blockN := *blockFlag
	if blockN == 0 {
		c, _ := tx.Cursor(kv.HeaderCanonical)
		k, _, lerr := c.Last()
		c.Close()
		if lerr != nil || len(k) != 8 {
			fmt.Fprintln(os.Stderr, "cannot find canonical head")
			os.Exit(1)
		}
		blockN = binary.BigEndian.Uint64(k)
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
				}
			}
		}
		changes = append(changes, ch)
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
