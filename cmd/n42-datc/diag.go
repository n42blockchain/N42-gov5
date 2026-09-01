// n42-datc diag — dumps one block's decoded changesets (the builder's exact
// view) to pinpoint a leaf-history divergence found by verify bisection.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

// runFoldDiff compares the verifier's as-of-N account view (from the big DB's
// leaf history) against a reference DB built to exactly N+1 (whose
// HashedAccounts ARE the ground truth at N).
func runFoldDiff(args []string) {
	fs := flag.NewFlagSet("folddiff", flag.ExitOnError)
	out := fs.String("out", "", "big DATC DB")
	ref := fs.String("ref", "", "reference DB built to N+1")
	n := fs.Uint64("n", 0, "height")
	_ = fs.Parse(args)

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	open := func(p string) kv.RoDB {
		db, err := mdbxkv.NewMDBX(logger).Path(p).Label(kv.ChainDB).
			MapSize(512 * datasize.GB).Accede().Readonly().Open(context.Background())
		if err != nil {
			die("open %s: %v", p, err)
		}
		return db
	}
	bigDB := open(*out)
	defer bigDB.Close()
	refDB := bigDB
	if *ref != *out {
		refDB = open(*ref)
		defer refDB.Close()
	}
	btx, _ := bigDB.BeginRo(context.Background())
	defer btx.Rollback()
	rtx, _ := refDB.BeginRo(context.Background())
	defer rtx.Rollback()

	// Reference: all HashedAccounts rows.
	refRows := map[string][]byte{}
	c, _ := rtx.Cursor(modules.HashedAccounts)
	for k, v, e := c.First(); k != nil && e == nil; k, v, e = c.Next() {
		refRows[string(k)] = append([]byte{}, v...)
	}
	c.Close()

	// Fold view: every key's floor ≤ N from the big DB's leaf history.
	foldRows := map[string][]byte{}
	lc, _ := btx.Cursor(tDatcLeafA)
	var curKey, curVal []byte
	var have bool
	flush := func() {
		if curKey != nil && have && len(curVal) > 0 {
			foldRows[string(curKey)] = append([]byte{}, curVal...)
		}
	}
	for k, v, e := lc.First(); k != nil && e == nil; k, v, e = lc.Next() {
		if len(k) != 32+blkLen {
			continue
		}
		hk, blk := k[:32], uint64(binary.BigEndian.Uint32(k[32:]))
		if string(hk) != string(curKey) {
			flush()
			curKey, curVal, have = append([]byte{}, hk...), nil, false
		}
		if blk <= *n {
			curVal, have = append(curVal[:0], v...), true
		}
	}
	flush()
	lc.Close()

	fmt.Printf("ref=%d rows  fold=%d rows at N=%d\n", len(refRows), len(foldRows), *n)

	// Structural trace: refold the ref rows through the verifier's own
	// GenStructStep fold, capturing every emitted branch, and diff against the
	// ref DB's TrieAccount rows (ground truth). The first differing path is
	// the fold bug. Account storage roots come from the ref DB values verbatim
	// (decode → re-encode with the row's own storage root via the big DB's
	// history), so this isolates STRUCTURE from value derivation.
	q := &querier{tx: btx} // accFold/stoFold 0: pure leaf fold
	// load schedule from big DB meta
	if sv, _ := btx.GetOne(tDatcMeta, []byte("sched")); len(sv) >= 8 {
		for d := 0; d <= maxChgDepth && (d+1)*8 <= len(sv); d++ {
			q.sched.e[d] = binary.BigEndian.Uint64(sv[d*8:])
		}
	}
	myBranches := map[string][]byte{}
	root, ok, err := q.foldAtTraced(nil, nil, *n, myBranches)
	if err != nil {
		die("traced fold: %v", err)
	}
	fmt.Printf("traced fold root=%x ok=%v, captured %d branches\n", root[:8], ok, len(myBranches))

	refBranches := map[string][]byte{}
	tc, _ := rtx.Cursor(modules.TrieOfAccounts)
	for k, v, e := tc.First(); k != nil && e == nil; k, v, e = tc.Next() {
		refBranches[string(k)] = append([]byte{}, v...)
	}
	tc.Close()
	fmt.Printf("ref TrieAccount rows: %d\n", len(refBranches))
	shown := 0
	for p, rv := range refBranches {
		mv, ok := myBranches[p]
		if !ok {
			if shown < 8 {
				fmt.Printf("  BRANCH MISSING in fold: path=%x ref=%x\n", p, rv[:min(len(rv), 40)])
				shown++
			}
			continue
		}
		if string(mv) != string(rv) {
			if shown < 8 {
				fmt.Printf("  BRANCH DIFF path=%x refLen=%d foldLen=%d\n", p, len(rv), len(mv))
				rs, rt, rh, rhs, rroot := trie.UnmarshalTrieNode(rv)
				ms, mt, mh, mhs, mroot := trie.UnmarshalTrieNode(mv)
				fmt.Printf("    masks ref state=%04x tree=%04x hash=%04x rootLen=%d | fold state=%04x tree=%04x hash=%04x rootLen=%d\n",
					rs, rt, rh, len(rroot), ms, mt, mh, len(mroot))
				ri, mi := 0, 0
				for nib := 0; nib < 16; nib++ {
					bit := uint16(1) << nib
					var rhh, mhh []byte
					if rh&bit != 0 {
						rhh = rhs[ri*32 : ri*32+32]
						ri++
					}
					if mh&bit != 0 {
						mhh = mhs[mi*32 : mi*32+32]
						mi++
					}
					if string(rhh) != string(mhh) {
						fmt.Printf("    CHILD %x differs: ref=%x fold=%x\n", nib, rhh[:min(len(rhh), 8)], mhh[:min(len(mhh), 8)])
						// Dump the as-of-N fold leaves under this child prefix.
						childPrefix := p + string([]byte{byte(nib)})
						cnt := 0
						for fk := range foldRows {
							nibs := nibblesOf([]byte(fk))
							if len(nibs) >= len(childPrefix) && string(nibs[:len(childPrefix)]) == childPrefix {
								var a account.StateAccount
								_ = a.DecodeForStorage(foldRows[fk])
								sd := []byte(fk[:32])
								sroot, hasS, serr := q.nodeHashAt(sd, nil, *n)
								if hasS {
									a.Root = sroot
								} else {
									a.Root = emptyTrieRoot
								}
								buf := make([]byte, a.EncodingLengthForHashing())
								a.EncodeForHashing(buf)
								fmt.Printf("      leaf %x val=%x hasStorage=%v serr=%v n=%d ch=%x root=%x\n        rlp=%x\n",
									fk[:6], foldRows[fk], hasS, serr, a.Nonce, a.CodeHash[:4], a.Root[:8], buf)
								if hasS {
									sc, _ := btx.Cursor(tDatcLeafS)
									for sk, sv, se := sc.Seek(sd); sk != nil && se == nil; sk, sv, se = sc.Next() {
										if len(sk) < stoDomainLen || string(sk[:stoDomainLen]) != string(sd) {
											break
										}
										fmt.Printf("        STOR-HIST slot=%x block=%d val=%x\n",
											sk[stoDomainLen:stoDomainLen+6], binary.BigEndian.Uint32(sk[stoDomainLen+32:]), sv)
									}
									sc.Close()
								}
								cnt++
								if cnt > 6 {
									break
								}
							}
						}
						fmt.Printf("      (%d leaves under prefix %x)\n", cnt, childPrefix)
					}
				}
				shown++
			}
		}
	}
	for p := range myBranches {
		if _, ok := refBranches[p]; !ok && shown < 12 {
			fmt.Printf("  BRANCH EXTRA in fold: path=%x\n", p)
			shown++
		}
	}
	miss, extra, diff := 0, 0, 0
	for k, rv := range refRows {
		fv, ok := foldRows[k]
		if !ok {
			if miss < 5 {
				fmt.Printf("  MISSING in fold: %x ref=%x\n", k[:8], rv)
			}
			miss++
		} else if string(fv) != string(rv) {
			if diff < 5 {
				fmt.Printf("  VALUE DIFF %x: ref=%x fold=%x\n", k[:8], rv, fv)
			}
			diff++
		}
	}
	for k, fv := range foldRows {
		if _, ok := refRows[k]; !ok {
			if extra < 5 {
				fmt.Printf("  EXTRA in fold: %x val=%x\n", k[:8], fv)
			}
			extra++
		}
	}
	fmt.Printf("missing=%d extra=%d valuediff=%d\n", miss, extra, diff)
}

// runStor dumps HashedStorage rows under an addrHash prefix in a DB.
func runStor(args []string) {
	fs := flag.NewFlagSet("stor", flag.ExitOnError)
	db := fs.String("db", "", "DB dir")
	pfx := fs.String("prefix", "", "addrHash hex prefix")
	_ = fs.Parse(args)
	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	d, err := mdbxkv.NewMDBX(logger).Path(*db).Label(kv.ChainDB).
		MapSize(512 * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer d.Close()
	tx, _ := d.BeginRo(context.Background())
	defer tx.Rollback()
	want, err := hexDecode(*pfx)
	if err != nil {
		die("prefix: %v", err)
	}
	c, _ := tx.Cursor(modules.HashedStorage)
	defer c.Close()
	cnt := 0
	for k, v, e := c.Seek(want); k != nil && e == nil; k, v, e = c.Next() {
		if len(k) < len(want) || string(k[:len(want)]) != string(want) {
			break
		}
		fmt.Printf("HashedStorage %x = %x\n", k, v)
		cnt++
		if cnt > 10 {
			break
		}
	}
	fmt.Printf("(%d rows under %x)\n", cnt, want)
	// Also the account row.
	if len(want) == 32 {
		av, _ := tx.GetOne(modules.HashedAccounts, want)
		fmt.Printf("HashedAccount row: %x\n", av)
	}
}

func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var b byte
		if _, err := fmt.Sscanf(s[2*i:2*i+2], "%02x", &b); err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func runDiag(args []string) {
	fs := flag.NewFlagSet("diag", flag.ExitOnError)
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "")
	block := fs.Uint64("block", 0, "")
	_ = fs.Parse(args)

	acctTbl := openCS(*csDir, "acctcs")
	defer acctTbl.Close()
	storTbl := openCS(*csDir, "storcs")
	defer storTbl.Close()

	accBlob, err := acctTbl.Retrieve(*block)
	if err != nil {
		die("acctcs: %v", err)
	}
	stoBlob, err := storTbl.Retrieve(*block)
	if err != nil {
		die("storcs: %v", err)
	}
	fmt.Printf("block %d: acctcs %dB storcs %dB\n", *block, len(accBlob), len(stoBlob))

	if len(accBlob) > 0 {
		entries, err := ethel.DecodeAccountChanges(accBlob)
		if err != nil {
			die("decode acctcs: %v", err)
		}
		for _, e := range entries {
			var oldS, newS string
			if len(e.OldValue) == 0 {
				oldS = "ABSENT"
			} else {
				var a account.StateAccount
				if err := a.DecodeForStorage(e.OldValue); err == nil {
					oldS = fmt.Sprintf("n=%d b=%s ch=%x", a.Nonce, a.Balance.String(), a.CodeHash[:4])
				} else {
					oldS = fmt.Sprintf("len=%d undecodable", len(e.OldValue))
				}
			}
			if len(e.NewValue) == 0 {
				newS = "DELETED"
			} else {
				var a account.StateAccount
				if err := a.DecodeForStorage(e.NewValue); err == nil {
					newS = fmt.Sprintf("n=%d b=%s ch=%x", a.Nonce, a.Balance.String(), a.CodeHash[:4])
				} else {
					newS = fmt.Sprintf("len=%d undecodable", len(e.NewValue))
				}
			}
			fmt.Printf("  ACCT %x  old[%s] -> new[%s]  (newLen=%d)\n", e.Address, oldS, newS, len(e.NewValue))
		}
	}
	if len(stoBlob) > 0 {
		entries, err := ethel.DecodeStorageChanges(stoBlob)
		if err != nil {
			die("decode storcs: %v", err)
		}
		for _, e := range entries {
			var addr types.Address
			copy(addr[:], e.CompositeKey[:20])
			fmt.Printf("  STOR %x slot=%x  old=%x -> new=%x\n", addr, e.CompositeKey[20:28], e.OldValue, e.NewValue)
		}
	}
}
