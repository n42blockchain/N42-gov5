// Command n42-bmt-probe reads a --tree bmt converted chain's persisted BMT (from
// MDBX at the head block's header.Root) and full-scans it against PlainState to
// localize where the conversion's BMT diverges from a from-PlainState rebuild.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/bmt"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

type roStore struct{ tx kv.Tx }

func (s roStore) Get(h bmt.Hash) (bmt.NodeValue, error) {
	v, err := s.tx.GetOne("BMTNode", h[:])
	if err != nil {
		return nil, err
	}
	if v == nil {
		return nil, bmt.ErrNotFound
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}
func (roStore) Put(bmt.Hash, bmt.NodeValue) error { return nil }

func main() {
	datadir := flag.String("datadir", "D:/mainnet-bls-bmt2", "bmt converted datadir")
	mapGB := flag.Int("map.gb", 4096, "MDBX map GB")
	flag.Parse()

	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(filepath.Join(*datadir, "chaindata")).
		Label(kv.ChainDB).MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	head := *rawdb.ReadCurrentBlockNumber(tx)
	hash, _ := rawdb.ReadCanonicalHash(tx, head)
	hdr := rawdb.ReadHeader(tx, hash, head)
	root := hdr.Root
	fmt.Printf("head=%d header.Root=%x\n", head, root)

	tree := bmt.NewFromRoot(roStore{tx}, bmt.Hash(root))

	// full-scan PlainState accounts against the BMT.
	aMatch, aMiss, aDiff, nA := 0, 0, 0, 0
	c, _ := tx.Cursor(modules.Account)
	for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
		if len(k) != 20 {
			continue
		}
		nA++
		var a account.StateAccount
		if a.DecodeForStorage(v) != nil {
			continue
		}
		if a.CodeHash == (types.Hash{}) {
			a.CodeHash = emptyCodeHash
		}
		var addr types.Address
		copy(addr[:], k)
		want := commitment.EncodeAccountValue(&a)
		got, gerr := tree.Get(bmt.Hash(commitment.AccountKeyHash(addr)))
		switch {
		case gerr == bmt.ErrNotFound || got == nil:
			aMiss++
			if aMiss <= 5 {
				fmt.Printf("  [acct MISSING] %x\n", addr)
			}
		case string(got) == string(want):
			aMatch++
		default:
			aDiff++
			if aDiff <= 5 {
				fmt.Printf("  [acct DIFF] %x bmt=%x plain=%x\n", addr, got, want)
			}
		}
	}
	c.Close()

	// storage
	sMatch, sMiss, sDiff, nS := 0, 0, 0, 0
	sc, _ := tx.Cursor(modules.Storage)
	for k, v, e := sc.First(); k != nil && e == nil; k, v, e = sc.Next() {
		if len(k) != 52 || len(v) == 0 {
			continue
		}
		nS++
		var addr types.Address
		copy(addr[:], k[:20])
		var slot types.Hash
		copy(slot[:], k[20:52])
		val := new(uint256.Int).SetBytes(v)
		b := val.Bytes32()
		start := 0
		for start < 31 && b[start] == 0 {
			start++
		}
		want := b[start:]
		got, gerr := tree.Get(bmt.Hash(commitment.StorageKeyHash(addr, slot)))
		switch {
		case gerr == bmt.ErrNotFound || got == nil:
			sMiss++
			if sMiss <= 5 {
				fmt.Printf("  [stor MISSING] %x/%x\n", addr, slot)
			}
		case string(got) == string(want):
			sMatch++
		default:
			sDiff++
			if sDiff <= 5 {
				fmt.Printf("  [stor DIFF] %x/%x bmt=%x plain=%x\n", addr, slot, got, want)
			}
		}
	}
	sc.Close()

	fmt.Printf("ACCOUNTS (%d): match=%d MISSING=%d DIFF=%d\n", nA, aMatch, aMiss, aDiff)
	fmt.Printf("STORAGE  (%d): match=%d MISSING=%d DIFF=%d\n", nS, sMatch, sMiss, sDiff)

	// REVERSE check: walk every leaf in the persisted BMT and verify it
	// corresponds to a live PlainState entry. Stale leaves (present in the BMT
	// but absent from PlainState) pass the forward scan above yet make the
	// conversion's root diverge from a rebuild over only the live leaf set.
	expected := make(map[bmt.Hash]struct{}, nA+nS)
	c2, _ := tx.Cursor(modules.Account)
	for k, _, e := c2.First(); k != nil && e == nil; k, _, e = c2.Next() {
		if len(k) != 20 {
			continue
		}
		var addr types.Address
		copy(addr[:], k)
		expected[bmt.Hash(commitment.AccountKeyHash(addr))] = struct{}{}
	}
	c2.Close()
	sc2, _ := tx.Cursor(modules.Storage)
	for k, v, e := sc2.First(); k != nil && e == nil; k, v, e = sc2.Next() {
		if len(k) != 52 || len(v) == 0 {
			continue
		}
		var addr types.Address
		copy(addr[:], k[:20])
		var slot types.Hash
		copy(slot[:], k[20:52])
		expected[bmt.Hash(commitment.StorageKeyHash(addr, slot))] = struct{}{}
	}
	sc2.Close()

	var leafTotal, leafStale, maxDepth int
	staleShown := 0
	type leafKV struct {
		k bmt.Hash
		v []byte
	}
	var leaves []leafKV
	depthHist := map[int]int{}
	var walk func(h bmt.Hash, depth int)
	walk = func(h bmt.Hash, depth int) {
		if h == (bmt.Hash{}) {
			return
		}
		data, err := tx.GetOne("BMTNode", h[:])
		if err != nil || data == nil {
			return
		}
		switch {
		case len(data) > 0 && data[0] == 0x4C: // leaf
			leafTotal++
			depthHist[depth]++
			if depth > maxDepth {
				maxDepth = depth
			}
			var kh bmt.Hash
			copy(kh[:], data[1:33])
			vv := make([]byte, len(data)-33)
			copy(vv, data[33:])
			leaves = append(leaves, leafKV{kh, vv})
			if _, ok := expected[kh]; !ok {
				leafStale++
				if staleShown < 10 {
					fmt.Printf("  [STALE leaf] keyHash=%x value=%x\n", kh, data[33:])
					staleShown++
				}
			}
		case len(data) == 65 && data[0] == 0x49: // internal
			var l, rr bmt.Hash
			copy(l[:], data[1:33])
			copy(rr[:], data[33:65])
			walk(l, depth+1)
			walk(rr, depth+1)
		}
	}
	walk(bmt.Hash(root), 0)
	fmt.Printf("REVERSE: leavesInBMT=%d expectedLive=%d STALE=%d maxLeafDepth=%d\n", leafTotal, len(expected), leafStale, maxDepth)

	// In-process rebuild: feed the walked leaves into a fresh sequential-Put tree
	// (canonical path). If this root == conversion root, the conversion is
	// canonical and the external verifier is wrong; if it differs, the conversion
	// built a non-canonical structure from a correct leaf set.
	rebuild := func(order []int) bmt.Hash {
		ft := bmt.New(memStore{})
		for _, i := range order {
			_ = ft.Put(leaves[i].k, leaves[i].v)
		}
		return ft.Root()
	}
	walkOrder := make([]int, len(leaves))
	for i := range walkOrder {
		walkOrder[i] = i
	}
	rev := make([]int, len(leaves))
	for i := range rev {
		rev[i] = len(leaves) - 1 - i
	}
	sortedByKey := make([]int, len(leaves))
	copy(sortedByKey, walkOrder)
	sort.Slice(sortedByKey, func(a, b int) bool {
		return bytes.Compare(leaves[sortedByKey[a]].k[:], leaves[sortedByKey[b]].k[:]) < 0
	})
	// deterministic shuffle
	shuf := make([]int, len(leaves))
	copy(shuf, walkOrder)
	st := uint64(0x9E3779B97F4A7C15)
	for i := len(shuf) - 1; i > 0; i-- {
		st = st*6364136223846793005 + 1442695040888963407
		j := int((st >> 33) % uint64(i+1))
		shuf[i], shuf[j] = shuf[j], shuf[i]
	}
	fmt.Printf("REBUILD walkOrder   root=%x\n", rebuild(walkOrder))
	fmt.Printf("REBUILD reverse     root=%x\n", rebuild(rev))
	fmt.Printf("REBUILD sortedByKey root=%x\n", rebuild(sortedByKey))
	fmt.Printf("REBUILD shuffled    root=%x\n", rebuild(shuf))
}

type memStore map[bmt.Hash]bmt.NodeValue

func (m memStore) Get(h bmt.Hash) (bmt.NodeValue, error) {
	if v, ok := m[h]; ok {
		return v, nil
	}
	return nil, bmt.ErrNotFound
}
func (m memStore) Put(h bmt.Hash, v bmt.NodeValue) error { m[h] = v; return nil }

func die(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...); os.Exit(1) }
