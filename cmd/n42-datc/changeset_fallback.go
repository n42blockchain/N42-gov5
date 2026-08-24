// changeset_fallback / splice: repair resume-gap blocks whose primary
// acctcs/storcs freezer blob is empty (0-length OR count=0 stub) by sourcing the
// block's real delta from a SECONDARY erigon-style MDBX (AccountChangeSet/
// StorageChangeSet, e.g. D:/N42-hashed/chaindata). For each gap block we keep
// the block-origin OLD values (straight from the secondary changeset) and derive
// the post-block NEW value of every key: newVal = the OLD value recorded at the
// key's NEXT change, or — if the key never changes again up to the secondary
// head — the secondary's current HashedAccounts/HashedStorage value.
//
// Two consumers share this derivation:
//   - --changeset-fallback: decodeOne injects the derived delta at fold time
//     (the primary freezer is not touched).
//   - --splice-changesets:  the derived blobs are written INTO the primary
//     acctcs/storcs freezer (Append overwrite from the first gap block), making
//     the freezer self-complete. Affected tail segments are backed up first.
package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// fbBlock holds a gap block's repaired delta in RAW form so both consumers work:
// csA/csS carry the block-origin OLD values; newA/newS carry the post-block NEW
// values (raw bytes, nil = deleted/zeroed).
type fbBlock struct {
	csA  *changeset.ChangeSet      // account old values (key=addr20)
	csS  *changeset.ChangeSet      // storage old values (key=addr20||slot32)
	newA map[types.Address][]byte  // post-block account bytes (nil=deleted)
	newS map[string][]byte         // post-block slot value, key=addr20||slot32 (nil=zeroed)
}

// loadChangesetFallback finds gap blocks in [start,end) and derives their forward
// delta from dir into b.csFallback. Returns the gap block count.
func (b *builder) loadChangesetFallback(dir string, start, end uint64) (int, error) {
	t0 := time.Now()
	fdb := openN42Chain(dir, 8192)
	defer fdb.Close()
	ftx, err := fdb.BeginRo(context.Background())
	if err != nil {
		return 0, err
	}
	defer ftx.Rollback()

	b.csFallback = map[uint64]*fbBlock{}
	gapAddr := map[types.Address]bool{}
	gapStor := map[string]bool{}
	var gapLo, gapHi uint64 = ^uint64(0), 0

	accC, err := ftx.Cursor(modules.AccountChangeSet)
	if err != nil {
		return 0, err
	}
	defer accC.Close()
	stoC, err := ftx.Cursor(modules.StorageChangeSet)
	if err != nil {
		return 0, err
	}
	defer stoC.Close()

	hasFallback := func(c kv.Cursor, n uint64) bool {
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], n)
		k, _, _ := c.Seek(bk[:])
		return k != nil && binary.BigEndian.Uint64(k[:8]) == n
	}
	// "Effectively empty" primary = 2-byte LE count header == 0 (a resume-gap left
	// a 0-length blob OR a count=0 stub, e.g. block 25101866). Both mean the fold
	// sees no changes.
	primaryCount := func(blob []byte) int {
		if len(blob) < 2 {
			return 0
		}
		return int(binary.LittleEndian.Uint16(blob[:2]))
	}

	// Pass 1: locate gap blocks; seed each block's OLD-value ChangeSets and the
	// key sets to track through the sweep.
	for n := start; n < end; n++ {
		ab, _ := b.acctTbl.Retrieve(n)
		sb, _ := b.storTbl.Retrieve(n)
		if primaryCount(ab) != 0 || primaryCount(sb) != 0 {
			continue
		}
		if !hasFallback(accC, n) && !hasFallback(stoC, n) {
			continue // genuinely empty block (no state change)
		}
		fb := &fbBlock{
			csA:  changeset.NewAccountChangeSet(),
			csS:  changeset.NewStorageChangeSet(),
			newA: map[types.Address][]byte{},
			newS: map[string][]byte{},
		}
		b.csFallback[n] = fb
		if n < gapLo {
			gapLo = n
		}
		if n > gapHi {
			gapHi = n
		}
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], n)
		for k, v, e := accC.Seek(bk[:]); k != nil && e == nil; k, v, e = accC.Next() {
			bn, addrB, oldVal, derr := changeset.DecodeAccounts(k, v)
			if derr != nil {
				return 0, fmt.Errorf("decode acc cs %d: %w", n, derr)
			}
			if bn != n {
				break
			}
			var addr types.Address
			copy(addr[:], addrB)
			gapAddr[addr] = true
			_ = fb.csA.Add(append([]byte{}, addr[:]...), append([]byte{}, oldVal...))
		}
		for k, v, e := stoC.Seek(bk[:]); k != nil && e == nil; k, v, e = stoC.Next() {
			bn, comp, oldVal, derr := changeset.DecodeStorage(k, v)
			if derr != nil {
				return 0, fmt.Errorf("decode sto cs %d: %w", n, derr)
			}
			if bn != n {
				break
			}
			gapStor[string(comp)] = true
			_ = fb.csS.Add(append([]byte{}, comp...), append([]byte{}, oldVal...))
		}
	}
	if len(b.csFallback) == 0 {
		fmt.Printf("[fallback] no gap blocks in [%d,%d)\n", start, end)
		return 0, nil
	}
	fmt.Printf("[fallback] %d gap block(s) [%d,%d]: %d account keys, %d storage keys\n",
		len(b.csFallback), gapLo, gapHi, len(gapAddr), len(gapStor))

	latestA := func(addr types.Address) []byte {
		v, _ := ftx.GetOne(modules.HashedAccounts, crypto.Keccak256(addr[:]))
		return v
	}
	latestS := func(addr types.Address, slot types.Hash) []byte {
		var hk [64]byte
		copy(hk[:32], crypto.Keccak256(addr[:]))
		copy(hk[32:], crypto.Keccak256(slot[:]))
		v, _ := ftx.GetOne(modules.HashedStorage, hk[:])
		return v
	}

	// Pass 2 (account): forward sweep from gapLo. newVal of a pending gap entry =
	// OLD value at the key's next change; early-break once all resolved and past
	// the gap; unresolved tail -> latest().
	lastA := map[types.Address]uint64{}
	{
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], gapLo)
		for k, v, e := accC.Seek(bk[:]); k != nil && e == nil; k, v, e = accC.Next() {
			bn, addrB, oldVal, derr := changeset.DecodeAccounts(k, v)
			if derr != nil {
				return 0, derr
			}
			if bn > gapHi && len(lastA) == 0 {
				break
			}
			var addr types.Address
			copy(addr[:], addrB)
			if !gapAddr[addr] {
				continue
			}
			if p, ok := lastA[addr]; ok {
				b.csFallback[p].newA[addr] = append([]byte{}, oldVal...)
				delete(lastA, addr)
			}
			if bn >= gapLo && bn <= gapHi {
				lastA[addr] = bn
			}
		}
		for addr, p := range lastA {
			b.csFallback[p].newA[addr] = append([]byte{}, latestA(addr)...)
		}
	}

	// Pass 2 (storage): same, keyed by addr||slot.
	lastS := map[string]uint64{}
	{
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], gapLo)
		for k, v, e := stoC.Seek(bk[:]); k != nil && e == nil; k, v, e = stoC.Next() {
			bn, comp, oldVal, derr := changeset.DecodeStorage(k, v)
			if derr != nil {
				return 0, derr
			}
			if bn > gapHi && len(lastS) == 0 {
				break
			}
			ck := string(comp)
			if !gapStor[ck] {
				continue
			}
			if p, ok := lastS[ck]; ok {
				b.csFallback[p].newS[ck] = append([]byte{}, oldVal...)
				delete(lastS, ck)
			}
			if bn >= gapLo && bn <= gapHi {
				lastS[ck] = bn
			}
		}
		for ck, p := range lastS {
			comp := []byte(ck)
			var addr types.Address
			var slot types.Hash
			copy(addr[:], comp[:20])
			copy(slot[:], comp[20:52])
			b.csFallback[p].newS[ck] = append([]byte{}, latestS(addr, slot)...)
		}
	}

	fmt.Printf("[fallback] derived %d gap block delta(s) from %s in %s\n",
		len(b.csFallback), dir, time.Since(t0).Round(time.Millisecond))
	return len(b.csFallback), nil
}

// fbAccount decodes a gap block's post-block account bytes for an address (nil =
// deleted). Used by the decodeOne fold injection.
func (fb *fbBlock) fbAccount(addr types.Address) *account.StateAccount {
	v := fb.newA[addr]
	if len(v) == 0 {
		return nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(v); err != nil {
		die("fallback decode account %x: %v", addr, err)
	}
	return &a
}

// encodeAccountChanges / encodeStorageChanges render a gap block's repaired delta
// to the exact acctcs/storcs wire format the freezer/decodeOne use.
func (fb *fbBlock) encodeAccountChanges() []byte {
	return ethel.EncodeAccountChanges(fb.csA, func(addr types.Address) []byte {
		return fb.newA[addr]
	})
}

func (fb *fbBlock) encodeStorageChanges() []byte {
	return ethel.EncodeStorageChanges(fb.csS, func(addr types.Address, slot types.Hash) []byte {
		var comp [52]byte
		copy(comp[:20], addr[:])
		copy(comp[20:], slot[:])
		return fb.newS[string(comp[:])]
	})
}

// injectGapBlock fills a pooled decodedBlock from a gap block's repaired delta,
// computing the addr/slot keccaks via the supplied helpers (mirrors decodeOne).
func (fb *fbBlock) injectGapBlock(d *decodedBlock, hashAddr func(types.Address), hashSlot func(types.Hash)) {
	for addr := range fb.newA {
		hashAddr(addr)
		d.dirtyA[addr] = fb.fbAccount(addr)
	}
	for ck := range fb.newS {
		comp := []byte(ck)
		var addr types.Address
		var slot types.Hash
		copy(addr[:], comp[:20])
		copy(slot[:], comp[20:52])
		deleted := false
		if a, ok := d.dirtyA[addr]; ok && a == nil {
			deleted = true
		}
		v := fb.newS[ck]
		if deleted && len(v) != 0 {
			continue // account deleted this block: drop writes, keep wipes
		}
		hashAddr(addr)
		hashSlot(slot)
		inner, ok := d.dirtyS[addr]
		if !ok {
			inner = d.innerMap()
			d.dirtyS[addr] = inner
		}
		if len(v) == 0 {
			inner[slot] = nil
		} else {
			inner[slot] = new(uint256.Int).SetBytes(v)
		}
	}
}

// ---------------------------------------------------------------------------
// --splice-changesets: write the derived gap-block deltas INTO the primary
// acctcs/storcs freezer, making it self-complete.

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// backupFreezerTail copies <name>.cidx and every <name>.NNNN.cdat segment from
// the one holding item gapLo to the last, into backupDir — the exact files an
// Append(gapLo) overwrite truncates/rewrites. items is the table's Items() (used
// to derive the cidx header size).
func backupFreezerTail(csDir, backupDir, name string, gapLo, items uint64) error {
	cidx := filepath.Join(csDir, name+".cidx")
	st, err := os.Stat(cidx)
	if err != nil {
		return err
	}
	hdr := st.Size() - int64(items)*int64(indexEntrySizeCidx) // 16 for N42 cidx
	if hdr < 0 {
		return fmt.Errorf("%s.cidx size %d < items*6 %d", name, st.Size(), items*6)
	}
	f, err := os.Open(cidx)
	if err != nil {
		return err
	}
	var e [6]byte
	if _, err := f.ReadAt(e[:], hdr+int64(gapLo)*int64(indexEntrySizeCidx)); err != nil {
		f.Close()
		return err
	}
	f.Close()
	startSeg := binary.BigEndian.Uint16(e[0:2])

	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return err
	}
	if err := copyFile(cidx, filepath.Join(backupDir, name+".cidx")); err != nil {
		return err
	}
	n := 0
	for seg := startSeg; ; seg++ {
		fn := fmt.Sprintf("%s.%04d.cdat", name, seg)
		src := filepath.Join(csDir, fn)
		if _, err := os.Stat(src); err != nil {
			break
		}
		if err := copyFile(src, filepath.Join(backupDir, fn)); err != nil {
			return err
		}
		n++
	}
	fmt.Printf("[splice] backed up %s.cidx + %d tail segment(s) (from %04d) to %s\n", name, n, startSeg, backupDir)
	return nil
}

const indexEntrySizeCidx = 6 // fileNum(2) + offset(4); mirrors freezer.indexEntrySize

// spliceChangesetsToFreezer derives the gap-block deltas from fallbackDir and
// writes them permanently into the primary acctcs/storcs freezer at csDir,
// backing up the affected tail segments to backupDir first. Non-gap tail blocks
// are read back and re-appended unchanged (Append-overwrite truncates from the
// first gap block, so the whole tail must be rewritten).
func (b *builder) spliceChangesetsToFreezer(fallbackDir, csDir, backupDir string, start, end uint64) error {
	nGap, err := b.loadChangesetFallback(fallbackDir, start, end)
	if err != nil {
		return err
	}
	if nGap == 0 {
		fmt.Println("[splice] no gap blocks — freezer already complete, nothing to do")
		return nil
	}
	gapLo := ^uint64(0)
	for k := range b.csFallback {
		if k < gapLo {
			gapLo = k
		}
	}
	last := b.acctTbl.Items()
	if s := b.storTbl.Items(); s != last {
		return fmt.Errorf("acctcs Items=%d != storcs Items=%d", last, s)
	}
	fmt.Printf("[splice] gapLo=%d last=%d → re-append %d block(s) (%d gap)\n", gapLo, last, last-gapLo, nGap)

	// Pre-mutation self-check: encode→decode every gap block and confirm the
	// round-trip matches the derived delta, so a bad encode can never corrupt the
	// freezer (we abort before touching disk).
	for n, fb := range b.csFallback {
		got := map[types.Address]string{}
		es, derr := ethel.DecodeAccountChanges(fb.encodeAccountChanges())
		if derr != nil {
			return fmt.Errorf("self-check block %d acct decode: %w", n, derr)
		}
		for _, e := range es {
			got[e.Address] = string(e.NewValue)
		}
		if len(got) != len(fb.newA) {
			return fmt.Errorf("self-check block %d acct count %d != %d", n, len(got), len(fb.newA))
		}
		for addr, nv := range fb.newA {
			if got[addr] != string(nv) {
				return fmt.Errorf("self-check block %d acct %x newVal mismatch", n, addr)
			}
		}
		sgot := map[string]string{}
		serr := ethel.DecodeStorageChangesFunc(fb.encodeStorageChanges(), func(a, s, _, nv []byte) error {
			var c [52]byte
			copy(c[:20], a)
			copy(c[20:], s)
			sgot[string(c[:])] = string(nv)
			return nil
		})
		if serr != nil {
			return fmt.Errorf("self-check block %d stor decode: %w", n, serr)
		}
		if len(sgot) != len(fb.newS) {
			return fmt.Errorf("self-check block %d stor count %d != %d", n, len(sgot), len(fb.newS))
		}
		for ck, nv := range fb.newS {
			if sgot[ck] != string(nv) {
				return fmt.Errorf("self-check block %d stor newVal mismatch", n)
			}
		}
	}
	fmt.Printf("[splice] encode/decode self-check passed for all %d gap blocks\n", nGap)

	// Back up the exact files the overwrite will truncate/rewrite.
	if err := backupFreezerTail(csDir, backupDir, "acctcs", gapLo, last); err != nil {
		return fmt.Errorf("backup acctcs: %w", err)
	}
	if err := backupFreezerTail(csDir, backupDir, "storcs", gapLo, last); err != nil {
		return fmt.Errorf("backup storcs: %w", err)
	}

	// Read the originals for [gapLo,last) BEFORE mutating (Append-overwrite drops
	// them). Gap blocks are replaced with the encoded delta; the rest are
	// re-appended verbatim (decompress→recompress preserves content).
	type pair struct{ a, s []byte }
	orig := make([]pair, last-gapLo)
	for i := gapLo; i < last; i++ {
		ab, e := b.acctTbl.Retrieve(i)
		if e != nil {
			return fmt.Errorf("retrieve acctcs %d: %w", i, e)
		}
		sb, e := b.storTbl.Retrieve(i)
		if e != nil {
			return fmt.Errorf("retrieve storcs %d: %w", i, e)
		}
		orig[i-gapLo] = pair{append([]byte{}, ab...), append([]byte{}, sb...)}
	}
	fmt.Printf("[splice] saved %d original tail block(s); opening freezer RW\n", last-gapLo)

	// Swap RO handles for RW (Close is double-close safe).
	b.acctTbl.Close()
	b.storTbl.Close()
	aw, err := freezer.NewFreezerTable(csDir, "acctcs", "c")
	if err != nil {
		return err
	}
	defer aw.Close()
	sw, err := freezer.NewFreezerTable(csDir, "storcs", "c")
	if err != nil {
		return err
	}
	defer sw.Close()

	for i := gapLo; i < last; i++ {
		var ab, sb []byte
		if fb, ok := b.csFallback[i]; ok {
			ab = fb.encodeAccountChanges()
			sb = fb.encodeStorageChanges()
		} else {
			ab = orig[i-gapLo].a
			sb = orig[i-gapLo].s
		}
		if err := aw.Append(i, ab); err != nil {
			return fmt.Errorf("append acctcs %d: %w", i, err)
		}
		if err := sw.Append(i, sb); err != nil {
			return fmt.Errorf("append storcs %d: %w", i, err)
		}
		if (i-gapLo)%50000 == 0 && i > gapLo {
			fmt.Printf("[splice] re-appended up to %d\n", i)
		}
	}
	if err := aw.Sync(); err != nil {
		return err
	}
	if err := sw.Sync(); err != nil {
		return err
	}
	fmt.Printf("[splice] DONE — freezer [%d,%d) rewritten with %d gap block(s) filled. "+
		"Verify: --bisect WITHOUT --changeset-fallback over the gap region.\n", gapLo, last, nGap)
	return nil
}
