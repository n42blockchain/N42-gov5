// snap-dictexp measures, on a real snapshot .val file, whether a zstd trained
// dictionary and/or a larger window meaningfully shrink the distributed
// .val.zst — the "手段2" question. It reports hard numbers only.
//
// The .val stream is [len:1B][fp:4B][value] per entry. The fp is random
// (incompressible); the value bytes are where any dictionary win must come
// from. We therefore report the payload breakdown and compress the WHOLE
// stream (the realistic pipeline unit) several ways.
package main

import (
	"fmt"
	"os"

	"github.com/klauspost/compress/zstd"
)

const fpBytes = 4

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: snap-dictexp <path/to/storage.sNN.val>")
		os.Exit(1)
	}
	raw, err := os.ReadFile(os.Args[1])
	must(err)
	fmt.Printf("input .val: %s (%.1f MB)\n", os.Args[1], mb(len(raw)))

	// Parse entries → payload breakdown + value samples for dict training.
	var nEntries, valBytes, fpTot, lenTot int
	var samples [][]byte
	const maxSamples = 300_000
	for i := 0; i < len(raw); {
		l := int(raw[i])
		i++
		if i+l > len(raw) {
			break
		}
		entry := raw[i : i+l]
		i += l
		nEntries++
		lenTot++
		fpTot += fpBytes
		val := entry[fpBytes:]
		valBytes += len(val)
		if len(samples) < maxSamples && len(val) > 0 {
			cp := make([]byte, len(val))
			copy(cp, val)
			samples = append(samples, cp)
		}
	}
	fmt.Printf("entries=%d  value=%.1fMB  fp=%.1fMB  len=%.1fMB  (fp is incompressible)\n\n",
		nEntries, mb(valBytes), mb(fpTot), mb(lenTot))

	// --- whole-stream compression (the real pipeline unit) ---
	base := compress(raw, 8<<20, nil)
	fmt.Printf("whole-stream  win=8MB    no-dict -> %.1f MB (%.2f%% of raw)   <- current pipeline\n",
		mb(base), pct(base, len(raw)))
	for _, winMB := range []int{32, 128} {
		szW := compress(raw, winMB<<20, nil)
		fmt.Printf("whole-stream  win=%-4dMB no-dict -> %.1f MB (%.2f%%)   Δ vs 8MB = %.2f MB\n",
			winMB, mb(szW), pct(szW, len(raw)), mb(base-szW))
	}

	// Build a dictionary. klauspost BuildDict needs a seed History (it is not a
	// COVER trainer): use a ~110 KB concat of sampled value bytes as the dict
	// content, and Contents as the training corpus.
	var hist []byte
	for _, s := range samples {
		if len(hist) >= 110<<10 {
			break
		}
		hist = append(hist, s...)
	}
	dict, derr := zstd.BuildDict(zstd.BuildDictOptions{
		ID: 1, History: hist, Contents: samples, Level: zstd.SpeedBestCompression,
	})
	if derr != nil {
		fmt.Printf("dict build failed: %v\n", derr)
	} else {
		szD := compress(raw, 8<<20, dict)
		fmt.Printf("whole-stream  win=8MB    dict(%.0fKB) -> %.1f MB (%.2f%%)   Δ vs 8MB = %.2f MB\n",
			mb(len(dict))*1024, mb(szD), pct(szD, len(raw)), mb(base-szD))

		// The ONE scenario a dict is built for: many SMALL independent frames
		// (block-addressable design). Compress the stream in 4 KB blocks,
		// each an independent frame, with vs without the dict.
		noDict := blockSum(raw, 4096, nil)
		wDict := blockSum(raw, 4096, dict)
		fmt.Printf("\n4KB-blocks (independent frames)  no-dict -> %.1f MB (%.2f%%)\n",
			mb(noDict), pct(noDict, len(raw)))
		fmt.Printf("4KB-blocks (independent frames)  dict    -> %.1f MB (%.2f%%)   dict saves %.2f MB here\n",
			mb(wDict), pct(wDict, len(raw)), mb(noDict-wDict))
	}
}

// blockSum compresses data in fixed-size independent frames and returns the
// total compressed bytes.
func blockSum(data []byte, block int, dict []byte) int {
	opts := []zstd.EOption{zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithWindowSize(1 << 20)}
	if dict != nil {
		opts = append(opts, zstd.WithEncoderDict(dict))
	}
	enc, err := zstd.NewWriter(nil, opts...)
	must(err)
	defer enc.Close()
	total := 0
	buf := make([]byte, 0, block)
	for i := 0; i < len(data); i += block {
		end := i + block
		if end > len(data) {
			end = len(data)
		}
		total += len(enc.EncodeAll(data[i:end], buf[:0]))
	}
	return total
}

func compress(data []byte, window int, dict []byte) int {
	opts := []zstd.EOption{
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithWindowSize(window),
	}
	if dict != nil {
		opts = append(opts, zstd.WithEncoderDict(dict))
	}
	enc, err := zstd.NewWriter(nil, opts...)
	must(err)
	out := enc.EncodeAll(data, nil)
	enc.Close()
	return len(out)
}

func mb(b int) float64  { return float64(b) / (1 << 20) }
func pct(a, b int) float64 { return float64(a) * 100 / float64(b) }
func must(err error) {
	if err != nil {
		panic(err)
	}
}
