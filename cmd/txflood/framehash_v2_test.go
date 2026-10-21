package main

import (
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/frame"
)

// TestFramesUnderHashV2: with N42_FRAME_HASH_V2 on, txflood's frames (built
// live and replayed from a corpus) verify at an ingest sink running the
// same setting and are stored with their entry hashes; a corpus signed
// under one hash version is refused under the other.
func TestFramesUnderHashV2(t *testing.T) {
	const total = 1234
	restore := frame.SetHashV2ForTest(false)
	defer restore()
	_, _, stop, fr := sinkServer(t)
	fr.total = total
	fr.rawAt = signedRawAt()
	fr.workers = 2
	v1Corpus := filepath.Join(t.TempDir(), "v1.bin")
	if err := fr.writeCorpus(v1Corpus); err != nil {
		t.Fatal(err)
	}
	stop()

	frame.SetHashV2ForTest(true)
	for _, mode := range []string{"live", "corpus"} {
		_, st, stop, fr := sinkServer(t)
		fr.total = total
		fr.rawAt = signedRawAt()
		fr.workers = 2
		if mode == "corpus" {
			p := filepath.Join(t.TempDir(), "v2.bin")
			if err := fr.writeCorpus(p); err != nil {
				t.Fatal(err)
			}
			c, err := openCorpus(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.check(1, false, total); err != nil {
				t.Fatal(err)
			}
			fr.corpus = c
		}
		res := fr.run(false)
		if res.Frames != 3 || res.Txs != total || res.Failed != 0 || st.Len() != 3 {
			t.Fatalf("%s: %s store=%d", mode, res, st.Len())
		}
		for k := int64(0); k < 3; k++ {
			bf := fr.buildFrame(k)
			h, err := frame.HashOf(bf.raw)
			if err != nil {
				t.Fatal(err)
			}
			e, ok := st.GetEntry(h)
			if !ok || len(e.TxHashes) != bf.ntx {
				t.Fatalf("%s: frame %d stored=%v entry hashes %d", mode, k, ok, len(e.TxHashes))
			}
			f, _ := frame.Decode(bf.raw)
			if got, err := f.Verify(); err != nil || got != h {
				t.Fatalf("%s: frame %d does not verify: %v", mode, k, err)
			}
		}
		stop()
	}

	c, err := openCorpus(v1Corpus)
	if err != nil {
		t.Fatal(err)
	}
	if c.check(1, false, total) == nil {
		t.Fatal("v1 corpus accepted under frame hash v2")
	}
	c.f.Close()
}
