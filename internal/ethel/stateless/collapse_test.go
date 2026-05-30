package stateless

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// TestDeleteHeavyCollapse stresses branch-collapse / extension-merge paths:
// build a trie, then delete a large random fraction of keys (the path most
// likely to expose reshape bugs), comparing partial-update to full rebuild.
func TestDeleteHeavyCollapse(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 200; round++ {
		nKeys := 5 + rng.Intn(120)
		base := map[string][]byte{}
		keys := make([][]byte, 0, nKeys)
		for i := 0; i < nKeys; i++ {
			k := k32(uint64(round)*1_000_000 + uint64(rng.Intn(nKeys*4)))
			if _, dup := base[string(k)]; dup {
				continue
			}
			base[string(k)] = []byte(fmt.Sprintf("v%d", i))
			keys = append(keys, k)
		}

		bt := fullTrie()
		for k, v := range base {
			if err := bt.update(keybytesToHex([]byte(k)), v); err != nil {
				t.Fatal(err)
			}
		}
		baseRoot := bt.hash()
		var proof [][]byte
		collectNodes(bt.root, &proof)
		if e := encodeNode(bt.root); len(e) < 32 {
			proof = append(proof, e)
		}

		// delete a random subset (sometimes ALL keys → root back to empty)
		changes := map[string][]byte{}
		delFrac := rng.Float64()
		for _, k := range keys {
			if rng.Float64() < delFrac {
				changes[string(k)] = nil
			}
		}
		if len(changes) == 0 {
			continue
		}

		// ground truth
		final := map[string][]byte{}
		for k, v := range base {
			final[k] = v
		}
		for k := range changes {
			delete(final, k)
		}
		ft := fullTrie()
		for k, v := range final {
			if err := ft.update(keybytesToHex([]byte(k)), v); err != nil {
				t.Fatal(err)
			}
		}
		fullRoot := ft.hash()

		// partial
		pt, err := newPartialTrie(baseRoot, proof)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		for k := range changes {
			if err := pt.update(keybytesToHex([]byte(k)), nil); err != nil {
				t.Fatalf("round %d: delete: %v", round, err)
			}
		}
		if !bytes.Equal(pt.hash(), fullRoot) {
			t.Fatalf("round %d (deleted %d/%d): partial %x != full %x",
				round, len(changes), len(keys), pt.hash()[:8], fullRoot[:8])
		}
	}
}
