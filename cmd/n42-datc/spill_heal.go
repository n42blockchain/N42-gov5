// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// spill-heal — recover rows finalize wrongly dropped from a .zspill bucket.
//
// finalizeBucket resyncs on the 4-byte zstd magic (28 B5 2F FD). But a spill
// ROW starts uvarint(keyLen)+key, and account keys are 40 bytes → every row in
// bucket a.b5 begins 28 B5. A key starting b5 2f fd emitted as raw literals in
// the compressed stream forms a FALSE magic that splits a healthy frame: both
// halves fail to decode and the whole real frame's rows are discarded as
// "corrupt". Bucket a.b5's 61 skipped frames (vs 2 kill-tail frames everywhere
// else) carry exactly this signature — and are fully recoverable.
//
// The heal: at each candidate frame start, if decoding to the next candidate
// fails, EXTEND the end across subsequent candidates until one decodes — false
// magics inside the frame are then skipped instead of splitting it. Only a
// genuinely truncated/corrupt frame (kill-tail) stays unrecoverable.
//
// Modes:
//	--diff-path X    audit: rows under nibble path X in spill vs the live segment
//	--emit DIR       repair: write healed bucket as DIR/<bucket>.zspill for finalize-leaves
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

func runSpillHeal(args []string) {
	fs := flag.NewFlagSet("spill-heal", flag.ExitOnError)
	spill := fs.String("spill", "", "one .zspill file (e.g. the backup's a.b5.zspill)")
	diffPath := fs.String("diff-path", "", "nibble path (hex) to report rows for (e.g. b50007)")
	at := fs.Uint64("at", 0, "report split: rows ≤ at vs > at (0 = no split)")
	emit := fs.String("emit", "", "write the healed rows as <emit>/<bucketname>.zspill (single clean frame set) for finalize-leaves")
	_ = fs.Parse(args)
	if *spill == "" {
		die("--spill required")
	}

	f, err := os.Open(*spill)
	if err != nil {
		die("open: %v", err)
	}
	comp, err := io.ReadAll(f)
	f.Close()
	if err != nil {
		die("read: %v", err)
	}
	fmt.Printf("spill-heal %s: %d compressed bytes\n", filepath.Base(*spill), len(comp))

	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		die("zstd: %v", err)
	}
	defer zr.Close()

	zstdMagic := []byte{0x28, 0xb5, 0x2f, 0xfd}
	var cand []int
	for i := 0; i+4 <= len(comp); {
		j := bytes.Index(comp[i:], zstdMagic)
		if j < 0 {
			break
		}
		cand = append(cand, i+j)
		i += j + 4
	}
	cand = append(cand, len(comp)) // sentinel end
	fmt.Printf("%d candidate frame starts\n", len(cand)-1)

	// Heal walk: from each start, find the shortest candidate end that decodes.
	var raw []byte
	healed, plain, lost := 0, 0, 0
	var lostSpans []string
	for fi := 0; fi < len(cand)-1; {
		start := cand[fi]
		decoded := false
		for ei := fi + 1; ei < len(cand); ei++ {
			dec, derr := zr.DecodeAll(comp[start:cand[ei]], nil)
			if derr == nil {
				raw = append(raw, dec...)
				if ei == fi+1 {
					plain++
				} else {
					healed++ // skipped ei-fi-1 false magics inside this frame
				}
				fi = ei
				decoded = true
				break
			}
			// Bound the extension: a real frame is ~256 KiB raw / ~1 MB comp.
			if cand[ei]-start > 64<<20 {
				break
			}
		}
		if !decoded {
			lost++
			if len(lostSpans) < 8 {
				lostSpans = append(lostSpans, fmt.Sprintf("[%d,%d)", start, cand[fi+1]))
			}
			fi++
		}
	}
	fmt.Printf("frames: %d plain, %d healed (false-magic splits), %d LOST (true corruption) %v\n",
		plain, healed, lost, lostSpans)

	// Parse rows.
	type row struct{ k, v []byte }
	var rows []row
	partial := 0
	for p := 0; p < len(raw); {
		kl, m := binary.Uvarint(raw[p:])
		if m <= 0 || kl > uint64(len(raw)-p) {
			partial++
			break
		}
		ks := p + m
		ke := ks + int(kl)
		if ke > len(raw) {
			partial++
			break
		}
		vl, m2 := binary.Uvarint(raw[ke:])
		if m2 <= 0 || vl > uint64(len(raw)-ke) {
			partial++
			break
		}
		ve := ke + m2 + int(vl)
		if ve > len(raw) {
			partial++
			break
		}
		rows = append(rows, row{k: raw[ks:ke], v: raw[ke+m2 : ve]})
		p = ve
	}
	fmt.Printf("%d rows recovered (%d partial-tail stops)\n", len(rows), partial)

	if *diffPath != "" {
		p, err := parseNibbleHex(*diffPath)
		if err != nil {
			die("--diff-path: %v", err)
		}
		bytePrefix := make([]byte, 0, len(p)/2)
		for i := 0; i+1 < len(p); i += 2 {
			bytePrefix = append(bytePrefix, p[i]<<4|p[i+1])
		}
		odd := len(p)%2 == 1
		nLE, nGT := 0, 0
		for _, r := range rows {
			if !bytes.HasPrefix(r.k, bytePrefix) {
				continue
			}
			if odd && len(r.k) > len(bytePrefix) && r.k[len(bytePrefix)]>>4 != p[len(p)-1] {
				continue
			}
			blk := uint64(0)
			if len(r.k) >= 8 {
				blk = binary.BigEndian.Uint64(r.k[len(r.k)-8:])
			}
			if *at > 0 && blk > *at {
				nGT++
				continue
			}
			nLE++
			fmt.Printf("  row key=%x blk=%d vlen=%d val=%x\n", r.k[:len(r.k)-8], blk, len(r.v), r.v)
		}
		fmt.Printf("under %s: %d rows ≤ %d shown, %d rows > (hidden)\n", *diffPath, nLE, *at, nGT)
	}

	if *emit != "" {
		if err := os.MkdirAll(*emit, 0o755); err != nil {
			die("emit dir: %v", err)
		}
		dst := filepath.Join(*emit, filepath.Base(*spill))
		zw, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(2))
		if err != nil {
			die("zstd writer: %v", err)
		}
		var buf bytes.Buffer
		scratch := make([]byte, 0, 128)
		var frame bytes.Buffer
		flushFrame := func() {
			if frame.Len() == 0 {
				return
			}
			buf.Write(zw.EncodeAll(frame.Bytes(), nil))
			frame.Reset()
		}
		for _, r := range rows {
			scratch = scratch[:0]
			scratch = binary.AppendUvarint(scratch, uint64(len(r.k)))
			scratch = append(scratch, r.k...)
			scratch = binary.AppendUvarint(scratch, uint64(len(r.v)))
			scratch = append(scratch, r.v...)
			frame.Write(scratch)
			if frame.Len() >= 256<<10 {
				flushFrame()
			}
		}
		flushFrame()
		if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
			die("emit: %v", err)
		}
		fmt.Printf("healed spill written: %s (%d bytes, %d rows)\n", dst, buf.Len(), len(rows))
	}
}
