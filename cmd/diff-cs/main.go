// diff-cs: compare n42 freezer changesets (storcs + acctcs) against reth's
// StorageChangeSets + AccountChangeSets over a block range.
//
// Two-level comparison:
//   1. Key presence: which (addr, slot) pairs each side records as changed
//      this block. Mismatch ⇒ one side missed a write or wiped extra slots.
//   2. OldValue bytes: the per-block "value at start of block" for each
//      (addr, slot). Mismatch with same key set ⇒ the two clients disagree
//      about what the slot held at block start, i.e. some prior block
//      already diverged. This is the bisect signal we want.
//
// Reth StorageChangeSets layout (DupSort):
//   key = block#(8B BE) || addr(20B)        // 28B
//   value = slot(32B) || compact-BE U256    // 32B + 0..32B
// Reth's `Compact for U256` writes `to_be_bytes()[32-size..]` — i.e. the
// trailing N bytes of the 32-byte big-endian representation, with leading
// zero bytes trimmed. So the bytes are already BE-minimal; do not reverse.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	tblStorCS = "StorageChangeSets"
	tblAcctCS = "AccountChangeSets"
)

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[tblStorCS] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tblAcctCS] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

type storKey struct {
	addr [20]byte
	slot [32]byte
}

type storEntry struct {
	oldVal []byte // BE-minimal, leading zeros trimmed
	newVal []byte // BE-minimal (n42 only — reth changesets store oldVal only)
}

func main() {
	n42Dir := flag.String("n42", "", "n42 freezer dir (contains acctcs/storcs), e.g. d:\\N42-rerun\\chain\\freezer — REQUIRED")
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 17860000, "end block (inclusive)")
	showAll := flag.Bool("show-all", false, "show every diff slot, not just summary")
	firstDiff := flag.Bool("first-diff", true, "stop at the first divergent block")
	maxPrintPerBlock := flag.Int("max-print", 10, "max divergent slots to print per block")
	skipKeyOnly := flag.Bool("skip-key-only", false, "ignore key-set mismatches (e.g. account-deletion-only blocks); only flag oldVal mismatches")
	stopOnAcctCount := flag.Bool("stop-on-acct-count", false, "let account-count mismatches trigger first-diff stop (default: account count is noisy — different clients track 'touched' differently — so off by default)")
	skipZeroOldMissing := flag.Bool("skip-zero-old-missing", false, "ignore MISSING entries whose reth_old==0 (zero-pre-value 'first-write' records — clients differ on whether to emit these, not necessarily a state bug; keep scanning until a VAL DIFF or non-zero MISSING surfaces)")
	skipWipeStale := flag.Bool("skip-wipe-stale", false, "ignore EXTRA entries that look like 'wipe-stale' (n42 deletes a slot mainnet never knew about — symptom of a prior incomplete SELFDESTRUCT; doesn't usually move root if MPT delete folds correctly, so suppress to find the real signal downstream)")
	progressEvery := flag.Uint64("progress", 20000, "log progress every N blocks (0 = silent)")
	flag.Parse()

	if *n42Dir == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --n42 is required (e.g. --n42 d:\\N42-rerun\\chain\\freezer)")
		os.Exit(2)
	}
	stor, err := freezer.NewFreezerTableReadOnly(*n42Dir, "storcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer stor.Close()
	stor.ForceBatchSize(freezer.BatchSize)
	stor.SetCompressed(true)

	acct, err := freezer.NewFreezerTableReadOnly(*n42Dir, "acctcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acct.Close()
	acct.ForceBatchSize(freezer.BatchSize)
	acct.SetCompressed(true)

	// Cap --to at the n42 freezer's last *safe* block. The very last entry
	// can be a still-flushing or just-committed block whose freezer write
	// (item N) and progress marker update interleave with the compactor —
	// reading it concurrently with a running ethexec yields an empty or
	// torn payload that looks like "n42 missing 100% of reth's entries".
	// We back off by 1 entry to dodge that boundary artifact.
	n42MaxBlock := stor.Items()
	if storItems := acct.Items(); storItems < n42MaxBlock {
		n42MaxBlock = storItems
	}
	if n42MaxBlock < 2 {
		fmt.Fprintln(os.Stderr, "n42 freezer has < 2 entries — has the rerun started?")
		os.Exit(1)
	}
	n42LastSafeBlock := n42MaxBlock - 2 // back off by 1 from the highest written entry
	if *toBlock > n42LastSafeBlock {
		fmt.Fprintf(os.Stderr, "WARNING: --to=%d exceeds n42 freezer's safe last block (%d). Capping to %d (skipping the in-flight tail).\n",
			*toBlock, n42LastSafeBlock, n42LastSafeBlock)
		*toBlock = n42LastSafeBlock
	}
	fmt.Fprintf(os.Stderr, "n42 freezer safe range: blocks [0, %d] (highest written %d, skipping tail)\n",
		n42LastSafeBlock, n42MaxBlock-1)

	logger := log.New()
	// Readonly + Accede: attach to an existing DB even if another process
	// owns it. Accede skips the bucket-flag harmonization that a pure
	// Readonly() open still attempts and that fails when the DB is held
	// elsewhere or has a different on-disk flag profile.
	db, err := mdbx.NewMDBX(logger).
		Path(*rethDir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		Accede().
		WithTableCfg(rethCfg).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// Single persistent cursor — seek once, iterate forward across all blocks.
	// Reopening per block is wasteful at 800K-block scale.
	stoCur, err := tx.Cursor(tblStorCS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "stor cursor:", err)
		os.Exit(1)
	}
	defer stoCur.Close()
	acctCur, err := tx.Cursor(tblAcctCS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "acct cursor:", err)
		os.Exit(1)
	}
	defer acctCur.Close()

	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, *fromBlock)
	rk, rv, _ := stoCur.Seek(seek)
	ak, _, _ := acctCur.Seek(seek)

	fmt.Printf("Comparing blocks [%d, %d]\n", *fromBlock, *toBlock)
	fmt.Printf("n42=%s\nreth=%s\n", *n42Dir, *rethDir)

	totN42Sto, totRethSto := 0, 0
	totN42Acct, totRethAcct := 0, 0
	t0 := time.Now()

	for b := *fromBlock; b <= *toBlock; b++ {
		n42Sto := decodeN42Storage(stor, b)
		n42Acct := decodeN42AccountCount(acct, b)
		rethSto, nextRk, nextRv := drainRethStorage(stoCur, b, rk, rv)
		rethAcct, nextAk := countRethAccount(acctCur, b, ak)
		rk, rv = nextRk, nextRv
		ak = nextAk

		totN42Sto += len(n42Sto)
		totRethSto += len(rethSto)
		totN42Acct += n42Acct
		totRethAcct += rethAcct

		missing, extra, valDiffs := diffSto(n42Sto, rethSto)
		// Optionally drop zero-pre-value MISSING (noise from changeset
		// recording-policy differences, not state divergence).
		var nonZeroMissing []storKey
		zeroMissing := 0
		for _, k := range missing {
			if len(rethSto[k].oldVal) == 0 {
				zeroMissing++
				continue
			}
			nonZeroMissing = append(nonZeroMissing, k)
		}
		realMissing := missing
		if *skipZeroOldMissing {
			realMissing = nonZeroMissing
		}
		// Optionally drop wipe-stale EXTRA (n42 deleting slots mainnet never
		// knew about — symptom but not necessarily a root-mover).
		var significantExtra []storKey
		wipeStaleExtra := 0
		for _, k := range extra {
			ne := n42Sto[k]
			if len(ne.newVal) == 0 && len(ne.oldVal) > 0 {
				wipeStaleExtra++
				continue
			}
			significantExtra = append(significantExtra, k)
		}
		realExtra := extra
		if *skipWipeStale {
			realExtra = significantExtra
		}
		hasKeyDiff := len(realMissing) > 0 || len(realExtra) > 0
		hasValDiff := len(valDiffs) > 0
		acctDiff := n42Acct != rethAcct
		// "Real" divergence = storage signal. Account-count mismatches are
		// expected noise (different clients touch a different set of system
		// accounts per block) and shouldn't drown out the real find.
		realDiff := hasValDiff || hasKeyDiff
		if *stopOnAcctCount {
			realDiff = realDiff || acctDiff
		}
		// Carry through for callers; we still print all entries by default.
		_ = zeroMissing
		_ = wipeStaleExtra

		// Block is "interesting" only when the storage signal fires (or when
		// the user explicitly opted into account-count stops). Account-count
		// mismatches are persistent noise (different clients touch different
		// system accounts each block) and printing them drowns out signal.
		if !*showAll && !realDiff {
			if *progressEvery > 0 && b%*progressEvery == 0 {
				fmt.Fprintf(os.Stderr, "  scanned to %d (%.0f blocks/s)\n",
					b, float64(b-*fromBlock+1)/time.Since(t0).Seconds())
			}
			continue
		}

		if *skipKeyOnly && !hasValDiff {
			continue
		}

		fmt.Printf("\nblock=%d n42_sto=%d reth_sto=%d n42_acct=%d reth_acct=%d  miss=%d extra=%d valDiffs=%d\n",
			b, len(n42Sto), len(rethSto), n42Acct, rethAcct,
			len(missing), len(extra), len(valDiffs))

		printed := 0
		for _, k := range valDiffs {
			if printed >= *maxPrintPerBlock {
				fmt.Printf("    ... and %d more value diffs\n", len(valDiffs)-printed)
				break
			}
			ne := n42Sto[k]
			rv := rethSto[k]
			// Categorize by terminal effect on PlainState:
			//   no-op:       n42 reads stale, writes same back. Buffer write
			//                makes our PlainState match the stale, mainnet
			//                stays at oldVal=0. Diverges silently.
			//   wipe-stale:  n42 wipes a stale slot to 0. Mainnet has 0.
			//                Both end at 0 — root MAY match if MPT delete
			//                folds the path away.
			//   real-diff:   n42 writes a new value, but our oldVal was
			//                stale. Diverges by oldVal-vs-mainnet-pre delta.
			tag := "real-diff"
			if bytesEqual(ne.oldVal, ne.newVal) {
				tag = "no-op (stale propagated to PlainState)"
			} else if len(ne.newVal) == 0 {
				tag = "wipe-stale"
			}
			fmt.Printf("    [VAL DIFF] addr=%x slot=%x n42_old=%x n42_new=%x reth_old=%x  (%s)\n",
				k.addr, k.slot, ne.oldVal, ne.newVal, rv.oldVal, tag)
			printed++
		}
		printed = 0
		for _, k := range missing {
			if printed >= *maxPrintPerBlock {
				fmt.Printf("    ... and %d more missing\n", len(missing)-printed)
				break
			}
			fmt.Printf("    [n42 MISSING ] addr=%x slot=%x reth_old=%x\n",
				k.addr, k.slot, rethSto[k].oldVal)
			printed++
		}
		printed = 0
		for _, k := range extra {
			if printed >= *maxPrintPerBlock {
				fmt.Printf("    ... and %d more extra\n", len(extra)-printed)
				break
			}
			ne := n42Sto[k]
			// Categorize: reth implicitly says pre==post==0 for slots not in
			// its changeset. So:
			//   newVal==oldVal → no-op record (root NOT affected)
			//   newVal==0      → we wipe a stale residual to 0 (root affected:
			//                    we emit a delete-row that mainnet never knew
			//                    about; trie shape may differ even when both
			//                    sides logically have "no slot")
			//   else           → genuine state divergence (root affected)
			tag := "ROOT-AFFECTING"
			if bytesEqual(ne.oldVal, ne.newVal) {
				tag = "no-op"
			} else if len(ne.newVal) == 0 {
				tag = "wipe-stale"
			}
			fmt.Printf("    [n42 EXTRA   ] addr=%x slot=%x n42_old=%x n42_new=%x  (%s)\n",
				k.addr, k.slot, ne.oldVal, ne.newVal, tag)
			printed++
		}

		if *firstDiff && realDiff {
			fmt.Printf("\nfirst-diff stop at block %d (elapsed %v)\n", b, time.Since(t0))
			fmt.Printf("\n=== totals up to first diff ===\n")
			fmt.Printf("n42  storage rows: %d  account rows: %d\n", totN42Sto, totN42Acct)
			fmt.Printf("reth storage rows: %d  account rows: %d\n", totRethSto, totRethAcct)
			return
		}
	}
	fmt.Printf("\n=== totals ===\n")
	fmt.Printf("n42  storage rows: %d  account rows: %d\n", totN42Sto, totN42Acct)
	fmt.Printf("reth storage rows: %d  account rows: %d\n", totRethSto, totRethAcct)
	fmt.Printf("scan complete in %v\n", time.Since(t0))
}

// decodeN42Storage parses one block's storcs blob and returns the
// (addr, slot) → oldVal map. oldVal is stored BE-minimal.
func decodeN42Storage(tbl *freezer.FreezerTable, block uint64) map[storKey]storEntry {
	out := map[storKey]storEntry{}
	data, err := tbl.Retrieve(block)
	if err != nil || len(data) < 2 {
		return out
	}
	addrCount := int(binary.LittleEndian.Uint16(data[0:2]))
	pos := 2
	for g := 0; g < addrCount; g++ {
		if pos+22 > len(data) {
			break
		}
		var addr [20]byte
		copy(addr[:], data[pos:pos+20])
		pos += 20
		slotCount := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		for s := 0; s < slotCount; s++ {
			if pos+34 > len(data) {
				return out
			}
			var k storKey
			k.addr = addr
			copy(k.slot[:], data[pos:pos+32])
			pos += 32
			oldLen := int(data[pos])
			pos++
			if oldLen > 32 || pos+oldLen+1 > len(data) {
				return out
			}
			oldVal := append([]byte(nil), data[pos:pos+oldLen]...)
			pos += oldLen
			newLen := int(data[pos])
			pos++
			if newLen > 32 || pos+newLen > len(data) {
				return out
			}
			newVal := append([]byte(nil), data[pos:pos+newLen]...)
			pos += newLen
			out[k] = storEntry{
				oldVal: trimLeadingZeros(oldVal),
				newVal: trimLeadingZeros(newVal),
			}
		}
	}
	return out
}

func decodeN42AccountCount(tbl *freezer.FreezerTable, block uint64) int {
	data, err := tbl.Retrieve(block)
	if err != nil || len(data) < 2 {
		return 0
	}
	return int(binary.LittleEndian.Uint16(data[0:2]))
}

// drainRethStorage consumes cursor entries belonging to `block`, starting
// from the (k, v) the caller is positioned at. Returns the (addr, slot) →
// oldVal map for `block` and the next (k, v) past `block`.
func drainRethStorage(c kv.Cursor, block uint64, k, v []byte) (map[storKey]storEntry, []byte, []byte) {
	out := map[storKey]storEntry{}
	for k != nil {
		if len(k) < 28 || len(v) < 32 {
			var err error
			k, v, err = c.Next()
			_ = err
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk < block {
			var err error
			k, v, err = c.Next()
			_ = err
			continue
		}
		if blk > block {
			return out, k, v
		}
		var key storKey
		copy(key.addr[:], k[8:28])
		copy(key.slot[:], v[:32])
		// Pre-block value: reth Compact U256 stores BE-minimal (the trailing
		// non-zero bytes of the 32-byte BE representation). Already in our
		// canonical form — copy as-is and trim defensively.
		var be []byte
		if len(v) > 32 {
			be = append([]byte(nil), v[32:]...)
		}
		out[key] = storEntry{oldVal: trimLeadingZeros(be)}
		var err error
		k, v, err = c.Next()
		_ = err
	}
	return out, k, v
}

// countRethAccount drains cursor entries belonging to `block` and returns
// the count plus the next k past `block`.
func countRethAccount(c kv.Cursor, block uint64, k []byte) (int, []byte) {
	n := 0
	for k != nil {
		if len(k) < 8 {
			var err error
			k, _, err = c.Next()
			_ = err
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk < block {
			var err error
			k, _, err = c.Next()
			_ = err
			continue
		}
		if blk > block {
			return n, k
		}
		n++
		var err error
		k, _, err = c.Next()
		_ = err
	}
	return n, k
}

func diffSto(n42 map[storKey]storEntry, reth map[storKey]storEntry) (missing, extra, valDiffs []storKey) {
	for k, rv := range reth {
		nv, ok := n42[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if !bytesEqual(nv.oldVal, rv.oldVal) {
			valDiffs = append(valDiffs, k)
		}
	}
	for k := range n42 {
		if _, ok := reth[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Slice(missing, func(i, j int) bool { return lessKey(missing[i], missing[j]) })
	sort.Slice(extra, func(i, j int) bool { return lessKey(extra[i], extra[j]) })
	sort.Slice(valDiffs, func(i, j int) bool { return lessKey(valDiffs[i], valDiffs[j]) })
	return
}

func lessKey(a, b storKey) bool {
	cmp := strings.Compare(string(a.addr[:]), string(b.addr[:]))
	if cmp != 0 {
		return cmp < 0
	}
	return strings.Compare(string(a.slot[:]), string(b.slot[:])) < 0
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func trimLeadingZeros(b []byte) []byte {
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	if i == 0 {
		return b
	}
	if i == len(b) {
		return nil
	}
	return b[i:]
}
