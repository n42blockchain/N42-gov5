// splice-cs — redesigned changeset (acctcs/storcs) freezer splice.
//
// Instead of in-place Append-overwrite of the live freezer, this builds a
// SEPARATE splice directory containing only the segments from the gap onward
// (NN..last) plus a full cidx — a drop-in replacement for the source's NN..last.
//
// The 43-block gap [25101824,25101866] is batch-64-aligned (gapLo = 392216*64)
// and fits inside ONE batch (392216), so the work is lightweight:
//   - byte-copy the source cidx prefix (header + first gapLo entries) and the
//     source segment NN's prefix [0, gapBatchOffset) into the splice dir;
//   - reopen as a FreezerTable (resumes at item=gapLo, file=NN);
//   - re-encode ONLY batch 392216 (gap items = derived from D:/N42-hashed,
//     non-gap items = copied from source);
//   - raw-copy every later batch verbatim (no recompress);
//   - the splice dir holds no files numbered < NN.
//
// Read-only on the source — safe to run alongside a live build. See
// docs/ethel/_analysis-datc-freezer-splice.md §2.1/§3 for the full analysis.
//
// Usage:
//
//	splice-cs --src D:/N42-eth1177/chain/freezer \
//	          --hashed D:/N42-hashed/chaindata \
//	          --out D:/N42-eth1177-cs-spliced
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

const (
	gapLo          = 25101824
	gapHi          = 25101866
	batchSize      = 64
	cidxHeaderSize = 16
	indexEntrySize = 6
)

var cidxMagic = [4]byte{'N', 'C', 'I', 'X'}

// ---------------------------------------------------------------------------
// rawSource — raw cidx/cdat access (from cmd/splice-leaves).

type cidxEntry struct {
	fileNum uint16
	offset  uint32
}

type rawSource struct {
	dir, table string
	cidx       []byte
	files      map[uint16]*os.File
	sizes      map[uint16]int64
	items      uint64
}

func openRawSource(dir, table string) (*rawSource, error) {
	cidx, err := os.ReadFile(filepath.Join(dir, table+".cidx"))
	if err != nil {
		return nil, err
	}
	if len(cidx) >= cidxHeaderSize && cidx[0] == cidxMagic[0] && cidx[1] == cidxMagic[1] &&
		cidx[2] == cidxMagic[2] && cidx[3] == cidxMagic[3] {
		cidx = cidx[cidxHeaderSize:]
	}
	usable := (len(cidx) / indexEntrySize) * indexEntrySize
	return &rawSource{
		dir: dir, table: table, cidx: cidx[:usable],
		files: map[uint16]*os.File{}, sizes: map[uint16]int64{},
		items: uint64(usable) / indexEntrySize,
	}, nil
}

func (s *rawSource) close() {
	for _, f := range s.files {
		f.Close()
	}
}

func (s *rawSource) indexEntry(item uint64) cidxEntry {
	off := item * indexEntrySize
	return cidxEntry{
		fileNum: binary.BigEndian.Uint16(s.cidx[off : off+2]),
		offset:  binary.BigEndian.Uint32(s.cidx[off+2 : off+6]),
	}
}

func (s *rawSource) openFile(fn uint16) (*os.File, int64, error) {
	if f, ok := s.files[fn]; ok {
		return f, s.sizes[fn], nil
	}
	path := filepath.Join(s.dir, fmt.Sprintf("%s.%04d.cdat", s.table, fn))
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	fi, _ := f.Stat()
	s.files[fn] = f
	s.sizes[fn] = fi.Size()
	return f, fi.Size(), nil
}

// readBatchBlob returns the raw compressed bytes of batch b (items [b*64,(b+1)*64)).
func (s *rawSource) readBatchBlob(b uint64) ([]byte, error) {
	startItem := b * batchSize
	if startItem >= s.items {
		return nil, fmt.Errorf("batch %d start %d out of range (items=%d)", b, startItem, s.items)
	}
	se := s.indexEntry(startItem)
	f, fsize, err := s.openFile(se.fileNum)
	if err != nil {
		return nil, err
	}
	var endOffset uint32
	if next := startItem + batchSize; next < s.items {
		ne := s.indexEntry(next)
		if ne.fileNum == se.fileNum {
			endOffset = ne.offset
		} else {
			endOffset = uint32(fsize)
		}
	} else {
		endOffset = uint32(fsize)
	}
	if endOffset < se.offset || int64(endOffset) > fsize {
		return nil, fmt.Errorf("batch %d bad bounds [%d,%d) fsize=%d", b, se.offset, endOffset, fsize)
	}
	blob := make([]byte, endOffset-se.offset)
	if _, err := f.ReadAt(blob, int64(se.offset)); err != nil {
		return nil, err
	}
	return blob, nil
}

// ---------------------------------------------------------------------------
// §3 gap derivation (replicates cmd/n42-datc/changeset_fallback.go for [gapLo,gapHi]).

type gapDelta struct {
	accBlob, stoBlob []byte
}

func deriveGap(hashedDir string, srcAcct, srcStor *freezer.FreezerTable) (map[uint64]*gapDelta, error) {
	t0 := time.Now()
	db, err := mdbxkv.NewMDBX(log.New()).Path(hashedDir).Label(kv.ChainDB).
		MapSize(8 * datasize.TB).Accede().Readonly().Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("open hashed: %w", err)
	}
	defer db.Close()
	ftx, err := db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer ftx.Rollback()

	accC, _ := ftx.Cursor(modules.AccountChangeSet)
	defer accC.Close()
	stoC, _ := ftx.Cursor(modules.StorageChangeSet)
	defer stoC.Close()

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
	primaryCount := func(blob []byte) int {
		if len(blob) < 2 {
			return 0
		}
		return int(binary.LittleEndian.Uint16(blob[:2]))
	}

	type fb struct {
		csA, csS *changeset.ChangeSet
		newA     map[types.Address][]byte
		newS     map[string][]byte
	}
	gaps := map[uint64]*fb{}
	gapAddr := map[types.Address]bool{}
	gapStor := map[string]bool{}

	// Pass 1: seed OLD values per gap block.
	for n := uint64(gapLo); n <= gapHi; n++ {
		ab, _ := srcAcct.Retrieve(n)
		sb, _ := srcStor.Retrieve(n)
		if primaryCount(ab) != 0 || primaryCount(sb) != 0 {
			continue // primary already has data (not a gap)
		}
		f := &fb{csA: changeset.NewAccountChangeSet(), csS: changeset.NewStorageChangeSet(),
			newA: map[types.Address][]byte{}, newS: map[string][]byte{}}
		seeded := false
		var bk [8]byte
		binary.BigEndian.PutUint64(bk[:], n)
		for k, v, e := accC.Seek(bk[:]); k != nil && e == nil; k, v, e = accC.Next() {
			bn, addrB, oldVal, derr := changeset.DecodeAccounts(k, v)
			if derr != nil {
				return nil, derr
			}
			if bn != n {
				break
			}
			var addr types.Address
			copy(addr[:], addrB)
			gapAddr[addr] = true
			_ = f.csA.Add(append([]byte{}, addr[:]...), append([]byte{}, oldVal...))
			seeded = true
		}
		for k, v, e := stoC.Seek(bk[:]); k != nil && e == nil; k, v, e = stoC.Next() {
			bn, comp, oldVal, derr := changeset.DecodeStorage(k, v)
			if derr != nil {
				return nil, derr
			}
			if bn != n {
				break
			}
			gapStor[string(comp)] = true
			_ = f.csS.Add(append([]byte{}, comp...), append([]byte{}, oldVal...))
			seeded = true
		}
		if seeded {
			gaps[n] = f
		}
	}

	// Pass 2 account: forward sweep — newVal = OLD at next change, else latest.
	lastA := map[types.Address]uint64{}
	var bk [8]byte
	binary.BigEndian.PutUint64(bk[:], gapLo)
	for k, v, e := accC.Seek(bk[:]); k != nil && e == nil; k, v, e = accC.Next() {
		bn, addrB, oldVal, derr := changeset.DecodeAccounts(k, v)
		if derr != nil {
			return nil, derr
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
			gaps[p].newA[addr] = append([]byte{}, oldVal...)
			delete(lastA, addr)
		}
		if bn >= gapLo && bn <= gapHi {
			lastA[addr] = bn
		}
	}
	for addr, p := range lastA {
		gaps[p].newA[addr] = append([]byte{}, latestA(addr)...)
	}

	// Pass 2 storage.
	lastS := map[string]uint64{}
	binary.BigEndian.PutUint64(bk[:], gapLo)
	for k, v, e := stoC.Seek(bk[:]); k != nil && e == nil; k, v, e = stoC.Next() {
		bn, comp, oldVal, derr := changeset.DecodeStorage(k, v)
		if derr != nil {
			return nil, derr
		}
		if bn > gapHi && len(lastS) == 0 {
			break
		}
		ck := string(comp)
		if !gapStor[ck] {
			continue
		}
		if p, ok := lastS[ck]; ok {
			gaps[p].newS[ck] = append([]byte{}, oldVal...)
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
		gaps[p].newS[ck] = append([]byte{}, latestS(addr, slot)...)
	}

	// Encode + self-check (decode round-trip).
	out := map[uint64]*gapDelta{}
	var nAcc, nSto int
	for n, f := range gaps {
		accBlob := ethel.EncodeAccountChanges(f.csA, func(a types.Address) []byte { return f.newA[a] })
		stoBlob := ethel.EncodeStorageChanges(f.csS, func(a types.Address, s types.Hash) []byte {
			var comp [52]byte
			copy(comp[:20], a[:])
			copy(comp[20:], s[:])
			return f.newS[string(comp[:])]
		})
		if _, err := ethel.DecodeAccountChanges(accBlob); err != nil {
			return nil, fmt.Errorf("self-check acc %d: %w", n, err)
		}
		if err := ethel.DecodeStorageChangesFunc(stoBlob, func(_, _, _, _ []byte) error { return nil }); err != nil {
			return nil, fmt.Errorf("self-check sto %d: %w", n, err)
		}
		out[n] = &gapDelta{accBlob: accBlob, stoBlob: stoBlob}
		nAcc += f.csA.Len()
		nSto += f.csS.Len()
	}
	fmt.Printf("[derive] %d gap blocks [%d,%d]: %d acc keys, %d sto keys in %s\n",
		len(out), gapLo, gapHi, nAcc, nSto, time.Since(t0).Round(time.Millisecond))
	return out, nil
}

// ---------------------------------------------------------------------------
// §2.1 seeded batch-aligned splice into a fresh directory.

func copyByteRange(srcPath, dstPath string, n int64) error {
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.CopyN(out, in, n); err != nil {
		return err
	}
	return out.Sync()
}

func spliceTable(srcDir, outDir, table string, gaps map[uint64]*gapDelta, enc *zstd.Encoder) (uint16, uint64, error) {
	raw, err := openRawSource(srcDir, table)
	if err != nil {
		return 0, 0, err
	}
	defer raw.close()
	maxItems := raw.items

	srcRO, err := freezer.NewFreezerTableCompressedReadOnly(srcDir, table, "c")
	if err != nil {
		return 0, 0, err
	}
	defer srcRO.Close()

	ge := raw.indexEntry(gapLo)
	NN := ge.fileNum
	gapBatchOffset := ge.offset

	// Seed: cidx prefix (header + gapLo entries) + segment NN prefix [0, gapBatchOffset).
	if err := copyByteRange(filepath.Join(srcDir, table+".cidx"),
		filepath.Join(outDir, table+".cidx"), cidxHeaderSize+int64(gapLo)*indexEntrySize); err != nil {
		return 0, 0, fmt.Errorf("seed cidx: %w", err)
	}
	if err := copyByteRange(filepath.Join(srcDir, fmt.Sprintf("%s.%04d.cdat", table, NN)),
		filepath.Join(outDir, fmt.Sprintf("%s.%04d.cdat", table, NN)), int64(gapBatchOffset)); err != nil {
		return 0, 0, fmt.Errorf("seed cdat: %w", err)
	}

	dst, err := freezer.NewFreezerTableCompressed(outDir, table, "c")
	if err != nil {
		return 0, 0, fmt.Errorf("open dst: %w", err)
	}
	defer dst.Close()
	if dst.Items() != gapLo {
		return 0, 0, fmt.Errorf("%s: seeded dst items=%d, want %d", table, dst.Items(), gapLo)
	}

	// Re-encode the single gap batch (gapLo..gapLo+63).
	gapBatch := uint64(gapLo) / batchSize
	var payload []byte
	for i := uint64(gapLo); i < uint64(gapLo)+batchSize; i++ {
		var item []byte
		if g, ok := gaps[i]; ok {
			if table == "acctcs" {
				item = g.accBlob
			} else {
				item = g.stoBlob
			}
		} else {
			item, err = srcRO.Retrieve(i)
			if err != nil {
				return 0, 0, fmt.Errorf("%s retrieve non-gap %d: %w", table, i, err)
			}
		}
		var lp [4]byte
		binary.LittleEndian.PutUint32(lp[:], uint32(len(item)))
		payload = append(payload, lp[:]...)
		payload = append(payload, item...)
	}
	batchBlob := enc.EncodeAll(payload, nil)
	if err := dst.AppendBatchBlob(gapLo, batchSize, batchBlob); err != nil {
		return 0, 0, fmt.Errorf("%s append gap batch: %w", table, err)
	}

	// Raw-copy every later batch verbatim (natural 2GB rotation).
	lastFull := maxItems / batchSize
	for b := gapBatch + 1; b < lastFull; b++ {
		blob, err := raw.readBatchBlob(b)
		if err != nil {
			return 0, 0, fmt.Errorf("%s read batch %d: %w", table, b, err)
		}
		if err := dst.AppendBatchBlob(b*batchSize, batchSize, blob); err != nil {
			return 0, 0, fmt.Errorf("%s append batch %d: %w", table, b, err)
		}
	}
	// Final partial batch.
	if rem := maxItems - lastFull*batchSize; rem > 0 {
		blob, err := raw.readBatchBlob(lastFull)
		if err != nil {
			return 0, 0, fmt.Errorf("%s read final batch: %w", table, err)
		}
		if err := dst.AppendBatchBlob(lastFull*batchSize, int(rem), blob); err != nil {
			return 0, 0, fmt.Errorf("%s append final batch: %w", table, err)
		}
	}
	if err := dst.Sync(); err != nil {
		return 0, 0, err
	}
	if dst.Items() != maxItems {
		return 0, 0, fmt.Errorf("%s: dst items=%d, want %d", table, dst.Items(), maxItems)
	}
	return NN, maxItems, nil
}

// verifyTable reopens the splice dir and checks the gap items + boundaries.
func verifyTable(outDir, table string, gaps map[uint64]*gapDelta, NN uint16, maxItems uint64) error {
	t, err := freezer.NewFreezerTableCompressedReadOnly(outDir, table, "c")
	if err != nil {
		return err
	}
	defer t.Close()
	if t.Items() != maxItems {
		return fmt.Errorf("items=%d want %d", t.Items(), maxItems)
	}
	// Gap items decode to the derived blobs.
	for n, g := range gaps {
		want := g.accBlob
		if table == "storcs" {
			want = g.stoBlob
		}
		got, err := t.Retrieve(n)
		if err != nil {
			return fmt.Errorf("retrieve gap %d: %w", n, err)
		}
		if len(got) != len(want) {
			return fmt.Errorf("gap %d len got=%d want=%d", n, len(got), len(want))
		}
		for i := range got {
			if got[i] != want[i] {
				return fmt.Errorf("gap %d byte %d differs", n, i)
			}
		}
	}
	// Boundary + tail retrievable.
	for _, n := range []uint64{gapLo - 1, gapHi + 1, maxItems - 1} {
		if _, err := t.Retrieve(n); err != nil {
			return fmt.Errorf("retrieve boundary %d: %w", n, err)
		}
	}
	// No segment files numbered < NN.
	ents, _ := os.ReadDir(outDir)
	for _, e := range ents {
		var fn int
		if _, err := fmt.Sscanf(e.Name(), table+".%04d.cdat", &fn); err == nil && uint16(fn) < NN {
			return fmt.Errorf("unexpected pre-gap segment %s (< %04d)", e.Name(), NN)
		}
	}
	return nil
}

func main() {
	srcDir := flag.String("src", "D:/N42-eth1177/chain/freezer", "source changeset freezer dir")
	hashedDir := flag.String("hashed", "D:/N42-hashed/chaindata", "erigon MDBX for gap derivation")
	outDir := flag.String("out", "", "splice output dir (created; refuses overwrite)")
	flag.Parse()
	if *outDir == "" {
		fmt.Fprintln(os.Stderr, "--out required")
		os.Exit(2)
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	if gapLo%batchSize != 0 {
		fmt.Fprintf(os.Stderr, "gapLo %d not batch-aligned\n", gapLo)
		os.Exit(1)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for _, t := range []string{"acctcs", "storcs"} {
		if fi, err := os.Stat(filepath.Join(*outDir, t+".cidx")); err == nil && fi.Size() > 0 {
			fmt.Fprintf(os.Stderr, "out already has %s.cidx; remove first\n", t)
			os.Exit(1)
		}
	}

	srcAcct, err := freezer.NewFreezerTableCompressedReadOnly(*srcDir, "acctcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer srcAcct.Close()
	srcStor, err := freezer.NewFreezerTableCompressedReadOnly(*srcDir, "storcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer srcStor.Close()

	gaps, err := deriveGap(*hashedDir, srcAcct, srcStor)
	if err != nil {
		fmt.Fprintln(os.Stderr, "derive:", err)
		os.Exit(1)
	}

	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()

	for _, t := range []string{"acctcs", "storcs"} {
		t0 := time.Now()
		NN, maxItems, err := spliceTable(*srcDir, *outDir, t, gaps, enc)
		if err != nil {
			fmt.Fprintln(os.Stderr, "splice "+t+":", err)
			os.Exit(1)
		}
		fmt.Printf("[splice] %s: gap segment NN=%04d, items=%d, dir holds %04d..last in %s\n",
			t, NN, maxItems, NN, time.Since(t0).Round(time.Millisecond))
		if err := verifyTable(*outDir, t, gaps, NN, maxItems); err != nil {
			fmt.Fprintln(os.Stderr, "VERIFY "+t+" FAILED:", err)
			os.Exit(1)
		}
		fmt.Printf("[verify] %s: %d gap items match derived, boundaries OK, no pre-%04d segments\n",
			t, len(gaps), NN)
	}
	fmt.Println("SPLICE-CS DONE")
}
