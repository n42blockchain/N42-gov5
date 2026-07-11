// -stateaudit: PlainState vs QMDB-tree cross-check on a stopped store.
//
// The QMDB tree is the commitment the network verified (its root matches the
// applied marker's header root), so on a store wedged by a DETERMINISTIC
// state-root mismatch — it re-executes a block the whole network accepted and
// keeps computing the same wrong root — the tree is the ground truth and
// PlainState is the suspect execution input. Walking every PlainState row and
// comparing it against Tree.Get(keyHash) splits the failure in one pass:
//
//   - mismatching / extra / missing PlainState rows -> the execution INPUT is
//     wrong (an unwind/realign path corrupted the flat state) and the wrong
//     root follows from garbage-in;
//   - a byte-identical PlainState -> the flat input is clean, so the fault is
//     in the execution->ComputeRoot->tree path itself (e.g. an in-memory
//     index desync after RevertBlock — the "undo record is poisoned" shape).
//
// Direction 2 (tree keys absent from PlainState) is approximated by count:
// PlainState rows vs the tree's live-key count.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

const auditSampleLimit = 12

func stateAudit(dir string) {
	db, err := mdbxkv.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).
		MapSize(64 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fmt.Printf("open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Printf("begin: %v\n", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	an, ah, aok, _ := rawdb.ReadQMDBApplied(tx)
	if !aok {
		fmt.Println("no applied marker")
		return
	}
	hdr := rawdb.ReadHeader(tx, ah, an)
	rc := commitment.NewQMDBRootComputer()
	rc.SetCold(tx)
	if err := rc.LoadFrom(tx); err != nil {
		fmt.Printf("LoadFrom: %v\n", err)
		return
	}
	root := rc.Root()
	fmt.Printf("== %s marker=%d/%x\n", dir, an, ah[:8])
	if hdr != nil {
		fmt.Printf("tree root %x vs header root %x match=%v (tree is ground truth only when true)\n",
			root[:8], hdr.Root[:8], root == hdr.Root)
	}
	tree := rc.Tree()

	var (
		accRows, accMismatch, accMissingInTree int
		stoRows, stoMismatch, stoMissingInTree int
		samples                                []string
	)
	addSample := func(s string) {
		if len(samples) < auditSampleLimit {
			samples = append(samples, s)
		}
	}

	// Accounts: PlainState -> tree.
	ac, err := tx.Cursor(modules.Account)
	if err != nil {
		fmt.Printf("account cursor: %v\n", err)
		return
	}
	for k, v, cerr := ac.First(); k != nil; k, v, cerr = ac.Next() {
		if cerr != nil {
			fmt.Printf("account scan: %v\n", cerr)
			return
		}
		if len(k) != 20 {
			continue
		}
		accRows++
		var addr types.Address
		copy(addr[:], k)
		acct := new(account.StateAccount)
		if err := acct.DecodeForStorageV2(v); err != nil {
			addSample(fmt.Sprintf("acct %x: undecodable plain value: %v", addr, err))
			accMismatch++
			continue
		}
		want := commitment.EncodeAccountValue(acct)
		got, ok := tree.Get(qmdb.Hash(commitment.AccountKeyHash(addr)))
		switch {
		case !ok:
			accMissingInTree++
			addSample(fmt.Sprintf("acct %x: in PlainState (nonce=%d bal=%s) but NOT in tree", addr, acct.Nonce, acct.Balance.String()))
		case string(got) != string(want):
			accMismatch++
			g := new(account.StateAccount)
			gs := "?"
			if derr := commitment.DecodeAccountValue(got, g); derr == nil {
				gs = fmt.Sprintf("nonce=%d bal=%s", g.Nonce, g.Balance.String())
			}
			addSample(fmt.Sprintf("acct %x: plain nonce=%d bal=%s != tree %s", addr, acct.Nonce, acct.Balance.String(), gs))
		}
	}
	ac.Close()

	// Storage: PlainState -> tree. The table is AutoDupSort; be tolerant of
	// both cursor key shapes (20-byte addr + slot||value, or 52-byte composite).
	sc, err := tx.Cursor(modules.Storage)
	if err != nil {
		fmt.Printf("storage cursor: %v\n", err)
		return
	}
	for k, v, cerr := sc.First(); k != nil; k, v, cerr = sc.Next() {
		if cerr != nil {
			fmt.Printf("storage scan: %v\n", cerr)
			return
		}
		var addr types.Address
		var slot types.Hash
		var val []byte
		switch {
		case len(k) == 20 && len(v) > 32:
			copy(addr[:], k)
			copy(slot[:], v[:32])
			val = v[32:]
		case len(k) == 52:
			copy(addr[:], k[:20])
			copy(slot[:], k[20:])
			val = v
		default:
			continue
		}
		stoRows++
		var want [32]byte
		copy(want[32-len(val):], val) // plain stores the value left-trimmed
		got, ok := tree.Get(qmdb.Hash(commitment.StorageKeyHash(addr, slot)))
		switch {
		case !ok:
			stoMissingInTree++
			addSample(fmt.Sprintf("slot %x/%x: in PlainState (=%x) but NOT in tree", addr, slot[:8], val))
		case len(got) != 32 || string(got) != string(want[:]):
			stoMismatch++
			addSample(fmt.Sprintf("slot %x/%x: plain=%x tree=%x", addr, slot[:8], want, got))
		}
	}
	sc.Close()

	fmt.Printf("accounts: rows=%d mismatch=%d missing-in-tree=%d\n", accRows, accMismatch, accMissingInTree)
	fmt.Printf("storage:  rows=%d mismatch=%d missing-in-tree=%d\n", stoRows, stoMismatch, stoMissingInTree)
	lc := tree.LiveCount()
	fmt.Printf("tree live keys=%d vs plain rows=%d (delta=%d -> tree-only keys if positive)\n",
		lc, accRows+stoRows, int64(lc)-int64(accRows+stoRows))
	if len(samples) > 0 {
		fmt.Println("samples:")
		for _, s := range samples {
			fmt.Println("  " + s)
		}
	}
	if accMismatch+accMissingInTree+stoMismatch+stoMissingInTree == 0 {
		fmt.Println("VERDICT: PlainState is byte-consistent with the tree — the execution input is clean; suspect the execution->ComputeRoot->tree path (in-memory index desync).")
	} else {
		fmt.Println("VERDICT: PlainState diverges from the tree — the execution INPUT is corrupt; audit the unwind/realign flat-state writers.")
	}
}
