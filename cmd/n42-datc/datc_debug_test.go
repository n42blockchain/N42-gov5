package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

// TestDiagFirstMismatch is the diagnostic companion of the e2e harness: at
// the first height whose reconstructed root differs from the reference it
// prints the pure-fold root, the block's events, and a per-key diff of the
// leaf-history view against the replayed world (accounts and every
// contract's slots). DATC_DBG_WINDOW=1 runs it in window mode. It fails
// like the e2e tests do; the value is in the log when it does.
func TestDiagFirstMismatch(t *testing.T) {
	sc := getScenario(t)
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	out := t.TempDir()
	db, err := openDatcDB(log.New(), out, 4, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	writeFwd(t, db, sc)
	o := e2eOpts{sched: schedE0is1, batch: 48, stoCache: 64, window: os.Getenv("DATC_DBG_WINDOW") != ""}
	b := newTestBuilder(t, db, out, sc, o, 0)
	end := uint64(len(sc.blocks))
	if err := b.run(0, end, o.batch); err != nil {
		t.Fatal(err)
	}
	q, closeQ := openTestQuerier(t, db, out, o)
	defer closeQ()
	for n := uint64(0); n < end; n++ {
		root, _, _ := q.nodeHashAt(nil, nil, n)
		if root == sc.roots[n] {
			continue
		}
		q0 := &querier{tx: q.tx, sched: q.sched}
		froot, _, _ := q0.nodeHashAt(nil, nil, n)
		t.Errorf("first mismatch at %d: rec-root=%x fold-root=%x ref=%x", n, root[:6], froot[:6], sc.roots[n][:6])
		gb := sc.blocks[n]
		for a, acc := range gb.accs {
			t.Logf("  block %d acct %x -> nil=%v wiped=%d explicitWipes=%v slots=%d", n, a[:4], acc == nil, len(gb.wipedSlots[a]), gb.explicitWipes[a], len(gb.slots[a]))
		}
		// per-key diff: accounts (fold view vs replayed reference)
		leaves, err := q0.asOfLeaves(nil, nil, n)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string][]byte{}
		for _, lf := range leaves {
			got[string(lf.remainder[:64])] = []byte(lf.value.(interface{ RawBytes() []byte }).RawBytes())
		}
		allAddrs := append(append(append([]types.Address{}, sc.eoas...), sc.big...), sc.small...)
		want := map[string][]byte{}
		addrOf := map[string]types.Address{}
		for _, a := range allAddrs {
			acc := sc.accountAt(a, n)
			if acc == nil {
				continue
			}
			w := *acc
			w.Root = sc.storageRootAt(a, n)
			buf := make([]byte, w.EncodingLengthForHashing())
			w.EncodeForHashing(buf)
			ah := keccak(a[:])
			want[string(nibblesOfBytes(ah[:]))] = buf
			addrOf[string(nibblesOfBytes(ah[:]))] = a
		}
		t.Logf("  accounts: fold=%d want=%d", len(got), len(want))
		shown := 0
		for k, wv := range want {
			gv, ok := got[k]
			if !ok {
				if shown < 8 {
					aa := addrOf[k]
					t.Logf("    MISSING acct %x", aa[:4])
					shown++
				}
			} else if !bytes.Equal(gv, wv) && shown < 8 {
				aa := addrOf[k]
				t.Logf("    DIFF acct %x got=%x want=%x", aa[:4], gv, wv)
				shown++
			}
		}
		for k := range got {
			if _, ok := want[k]; !ok && shown < 12 {
				t.Logf("    EXTRA acct nib=%x", k[:8])
				shown++
			}
		}
		// storage diff for every contract
		for _, a := range append(append([]types.Address{}, sc.big...), sc.small...) {
			want := sc.storageAt(a, n)
			ah := keccak(a[:])
			dom := ah[:]
			sl, err := q0.asOfLeaves(dom, nil, n)
			if err != nil {
				t.Fatal(err)
			}
			wantByHash := map[[32]byte][]byte{}
			for s, v := range want {
				wantByHash[keccak(s[:])] = v
			}
			gotByNib := map[string][]byte{}
			for _, lf := range sl {
				gotByNib[string(lf.remainder[:64])] = []byte(lf.value.(interface{ RawBytes() []byte }).RawBytes())
			}
			if len(gotByNib) != len(wantByHash) {
				t.Logf("  contract %x: got %d slots want %d", a[:4], len(gotByNib), len(wantByHash))
			}
			for h, v := range wantByHash {
				g, ok := gotByNib[string(nibblesOfBytes(h[:]))]
				if !ok {
					t.Logf("    missing slot %x", h[:4])
				} else if !bytes.Equal(g, v) {
					t.Logf("    slot %x value got %x want %x", h[:4], g, v)
				}
			}
			for k := range gotByNib {
				var h [32]byte
				nb := []byte(k)
				for i := 0; i < 32; i++ {
					h[i] = nb[2*i]<<4 | nb[2*i+1]
				}
				if _, ok := wantByHash[h]; !ok {
					t.Logf("    extra slot %x", h[:4])
					comp := append(append([]byte{}, dom...), h[:]...)
					c, _ := q0.leafCursor(true)
					for k, v, e := c.Seek(comp); k != nil && e == nil && bytes.HasPrefix(k, comp); k, v, e = c.Next() {
						t.Logf("      hist block=%d val=%x", binary.BigEndian.Uint32(k[64:]), v)
					}
					c.Close()
				}
			}
			if len(gotByNib) != len(wantByHash) {
				for i := uint64(0); i <= n; i++ {
					gb := sc.blocks[i]
					acc, hasA := gb.accs[a]
					if !hasA && len(gb.slots[a]) == 0 {
						continue
					}
					t.Logf("    ev block %d: hasAcct=%v nil=%v wiped=%d explicit=%v slots=%d", i, hasA, hasA && acc == nil, len(gb.wipedSlots[a]), gb.explicitWipes[a], len(gb.slots[a]))
				}
			}
			acc := sc.accountAt(a, n)
			_ = acc
		}
		break
	}
	_ = account.NewAccount
}
