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
	"flag"
	"fmt"
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

func nopAccHC([]byte, uint16, uint16, uint16, []byte, []byte) error            { return nil }
func nopStorHC([]byte, []byte, uint16, uint16, uint16, []byte, []byte) error   { return nil }
func keccakB(b []byte) []byte                                                  { h, _ := types.HashData(b); return h[:] }
func emptyCodeHash() []byte                                                    { h, _ := types.HashData(nil); return h[:] }
func emptyStorageRoot() []byte                                                 { return keccakB([]byte{0x80}) } // keccak(rlp("")) = empty MPT root

func main() {
	csDir := flag.String("cs", `D:/N42-eth1177/chain/freezer`, "freezer dir with acctcs/storcs (V2 forward changesets)")
	hdrDir := flag.String("headers", `D:/n42-eth1/chain/freezer`, "columnar headerc dir")
	tmp := flag.String("tmp", filepath.Join(os.TempDir(), "n42-bp-trie"), "temp writable trie datadir (recreated)")
	out := flag.String("out", "", "anchorc freezer dir (default <tmp>/anchorfz)")
	endBlock := flag.Uint64("end", 20000, "build genesis..end (exclusive)")
	K := flag.Uint64("k", 1000, "emit a transition BlockProof every K blocks")
	mapGB := flag.Int("mapsize-gb", 64, "temp trie MDBX mapsize GB")
	flag.Parse()

	ctx := context.Background()
	logger := log.New()
	if *K == 0 {
		fmt.Fprintln(os.Stderr, "--k must be > 0")
		os.Exit(1)
	}
	if *out == "" {
		*out = filepath.Join(*tmp, "anchorfz")
	}
	_ = os.RemoveAll(*tmp)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	// anchorc fdb (zstd), same format as the full-window producer. item = n/K - 1;
	// strictly sequential — a skipped/failed anchor stores an EMPTY item to keep the
	// index aligned (the serve returns empty → client treats that K as unavailable).
	anchorTbl, err := freezer.NewFreezerTableCompressed(*out, "anchorc", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open anchorc freezer:", err)
		os.Exit(1)
	}
	defer anchorTbl.Close()

	db, err := mdbx.NewMDBX(logger).Path(filepath.Join(*tmp, "chaindata")).Label(kv.ChainDB).
		PageSize(4096).MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).Open(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open temp db:", err)
		os.Exit(1)
	}
	defer db.Close()

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

	tx, err := db.BeginRw(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin rw:", err)
		os.Exit(1)
	}
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false) // first block bootstraps; the rest incremental

	t0 := time.Now()
	var preRoot types.Hash // header[n-1].Root (empty before block 0)
	emitted, verifiedOK := 0, 0

	for n := uint64(0); n < *endBlock; n++ {
		dA, dS := readBlockChangeset(acctTbl, stoTbl, n)
		isAnchor := n > 0 && n%*K == 0

		var bp *stateless.BlockProof
		var bpErr error
		if isAnchor {
			// pre-trie is at header[n-1] (== preRoot). Capture block n's transition proof.
			bp, bpErr = buildBlockProof(tx, n, dA, dS)
		}

		root, cerr := trc.ComputeRoot(dA, dS)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "ComputeRoot %d: %v\n", n, cerr)
			os.Exit(1)
		}
		trc.SetIncremental(true)

		hdr, herr := hc.ReadHeader(n)
		if herr != nil {
			fmt.Fprintf(os.Stderr, "read header %d: %v\n", n, herr)
			os.Exit(1)
		}
		if root != hdr.Root {
			fmt.Fprintf(os.Stderr, "ROOT MISMATCH block %d: computed %s header %s\n", n, root.Hex(), hdr.Root.Hex())
			os.Exit(2)
		}

		if isAnchor {
			emitted++
			var wire []byte // empty = skipped/failed (keeps freezer item index aligned)
			if bpErr != nil {
				fmt.Fprintf(os.Stderr, "BLOCKPROOF n=%d SKIP (proof build): %v\n", n, bpErr)
			} else {
				verr := stateless.VerifyStateRoot(preRoot[:], root[:], bp)
				status := "✓ VERIFIED"
				if verr != nil {
					status = "✗ FAILED: " + verr.Error()
				} else {
					verifiedOK++
					wire = stateless.EncodeBlockProof(bp)
				}
				fmt.Fprintf(os.Stderr, "BLOCKPROOF n=%d changes=%d acctNodes=%d pre=%s→post=%s %s (%s)\n",
					n, len(bp.Changes), len(bp.AccountProof), preRoot.Hex()[:10], root.Hex()[:10], status,
					time.Since(t0).Truncate(time.Second))
			}
			if werr := anchorTbl.Append(n/(*K)-1, wire); werr != nil { // item = n/K - 1
				fmt.Fprintf(os.Stderr, "append anchor %d (item %d): %v\n", n, n/(*K)-1, werr)
				os.Exit(1)
			}
		}
		preRoot = root

		if (n+1)%*K == 0 { // commit periodically
			if err := tx.Commit(); err != nil {
				fmt.Fprintf(os.Stderr, "commit %d: %v\n", n, err)
				os.Exit(1)
			}
			tx, err = db.BeginRw(ctx)
			if err != nil {
				fmt.Fprintln(os.Stderr, "re-begin:", err)
				os.Exit(1)
			}
			trc.SetRwTx(tx)
		}
	}
	tx.Rollback()
	if serr := anchorTbl.Sync(); serr != nil {
		fmt.Fprintln(os.Stderr, "anchorc sync:", serr)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "DONE: %d BlockProofs emitted, %d/%d self-VERIFIED (every %d) → %s (anchorc fdb) in %s\n",
		emitted, verifiedOK, emitted, *K, *out, time.Since(t0).Truncate(time.Second))
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
// n at the PRE-state trie (tx is at header[n-1]): a pre-state account multiproof +
// per-account storage proofs over the touched keys, with the block's NEW-value
// changeset. Deleted keys get neighbour subtrees retained (forward delete collapses
// a branch — same sibling-node need as the reverse case).
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

	acctNodes := map[string][]byte{}
	addNodes := func(m map[string][]byte, ns [][]byte) {
		for _, nd := range ns {
			m[string(keccakB(nd))] = nd
		}
	}
	var changes []stateless.AccountChange
	var delAccts []types.Hash
	type sk struct{ a, s types.Hash }
	var delSlots []sk

	for _, addr := range addrs {
		addrHash, _ := types.HashData(addr[:])
		// Pre-state account (for the ProofRetainer hint + storage subtree).
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
		// A storage proof only exists when the account (and thus its storage
		// subtree) is present at pre-state. A contract CREATED in this block has
		// no pre storage subtree — its slots are pure inserts into an empty root.
		slotOrder := allSlots
		if !preExists {
			slotOrder = nil
		}

		rl := trie.NewRetainList(0)
		pr, perr := trie.NewProofRetainer(addr, &preAcc, slotOrder, rl)
		if perr != nil {
			return nil, fmt.Errorf("proof retainer %x: %w", addr[:6], perr)
		}
		loader := trie.NewFlatDBTrieLoader("bp", rl, nopAccHC, nopStorHC, false)
		loader.SetProofRetainer(pr)
		if _, lerr := loader.CalcTrieRoot(tx, nil); lerr != nil {
			return nil, fmt.Errorf("calc %x: %w", addr[:6], lerr)
		}
		res, rerr := pr.ProofResult()
		if rerr != nil {
			return nil, fmt.Errorf("proof result %x: %w", addr[:6], rerr)
		}
		addNodes(acctNodes, res.AccountProof)

		ch := stateless.AccountChange{AddrHash: addrHash}
		newAcc, inAcct := dA[addr]
		if inAcct && newAcc == nil {
			ch.Deleted = true
			delAccts = append(delAccts, addrHash)
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
			ch.Nonce = preAcc.Nonce
			ch.Balance.Set(&preAcc.Balance)
			chash := preAcc.CodeHash
			if chash == (types.Hash{}) {
				chash = types.BytesToHash(emptyCodeHash())
			}
			ch.CodeHash = chash[:]
		}
		if len(allSlots) > 0 {
			if preExists {
				ch.StorageRoot = res.StorageHash[:]
				stNodes := map[string][]byte{}
				for i := range res.StorageProof {
					for _, nd := range res.StorageProof[i].Proof {
						stNodes[string(keccakB(nd))] = nd
					}
				}
				for _, nd := range stNodes {
					ch.StorageProof = append(ch.StorageProof, nd)
				}
			} else {
				ch.StorageRoot = emptyStorageRoot() // created account: empty pre-subtree
			}
			for _, slot := range allSlots {
				slotHash, _ := types.HashData(slot[:])
				val := dS[addr][slot]
				var vb []byte
				if val != nil && !val.IsZero() {
					b := val.Bytes32()
					vb = bytes.TrimLeft(b[:], "\x00")
				}
				ch.Storage = append(ch.Storage, stateless.StorageChange{SlotHash: slotHash, Value: vb})
				if len(vb) == 0 && preExists { // slot deleted forward in an existing subtree → collapse
					delSlots = append(delSlots, sk{addrHash, slotHash})
				}
			}
		}
		changes = append(changes, ch)
	}

	// Neighbour-retain for FORWARD-deleted keys (branch collapse needs siblings).
	if len(delAccts) > 0 || len(delSlots) > 0 {
		rl := trie.NewRetainList(0)
		wr := trie.NewWitnessRetainer(rl)
		for _, ah := range delAccts {
			for _, nb := range acctNeighbors(tx, ah) {
				wr.AddHashedKey(nb)
			}
		}
		for _, ds := range delSlots {
			for _, nb := range slotNeighbors(tx, ds.a, ds.s) {
				var comp [64]byte
				copy(comp[:32], ds.a[:])
				copy(comp[32:], nb)
				wr.AddHashedKey(comp[:])
			}
		}
		loader := trie.NewFlatDBTrieLoader("bp-neighbors", rl, nopAccHC, nopStorHC, false)
		loader.SetWitnessRetainer(wr)
		if _, lerr := loader.CalcTrieRoot(tx, nil); lerr == nil {
			sib := wr.Nodes()
			for _, nd := range sib {
				acctNodes[string(keccakB(nd))] = nd
			}
			for i := range changes {
				if len(changes[i].StorageProof) > 0 {
					changes[i].StorageProof = append(changes[i].StorageProof, sib...)
				}
			}
		}
	}

	acctProof := make([][]byte, 0, len(acctNodes))
	for _, nd := range acctNodes {
		acctProof = append(acctProof, nd)
	}
	return &stateless.BlockProof{Number: n, AccountProof: acctProof, Changes: changes}, nil
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
