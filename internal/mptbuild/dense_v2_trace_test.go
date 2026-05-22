package mptbuild

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/trie"
)

// TestV2OriginTrace_TwoAccounts forces a small trie where we can see
// whether extension origins are captured into the DenseBranchSink's
// extMask. Uses 2 synthetic accounts; the trie is minimal but should
// still exercise the extension+branch case.
func TestV2OriginTrace_TwoAccounts(t *testing.T) {
	// Two synthetic accounts. Their keccak prefixes likely diverge at
	// some nibble — gen_struct_step may or may not emit an extension.
	entries := makeAccountEntries(500)

	type capture struct {
		keyHex    []byte
		stateMask uint16
		treeMask  uint16
		extMask   uint16
		slotData  []byte
	}
	var caps []capture

	tgt := &MapTarget{}
	res, err := Build(context.Background(), Opts{
		Source:    &MapSource{Entries: entries},
		Target:    tgt,
		Extractor: NewAccountExtractor(),
		TmpDir:    filepath.Join(t.TempDir(), "etl"),
		BufMB:     1,
		DenseBranchSink: func(keyHex []byte, stateMask, treeMask, extMask uint16, slotData []byte) error {
			cp := capture{
				keyHex:    append([]byte(nil), keyHex...),
				stateMask: stateMask,
				treeMask:  treeMask,
				extMask:   extMask,
				slotData:  append([]byte(nil), slotData...),
			}
			caps = append(caps, cp)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Logf("stateRoot=%x branches=%d", res.StateRoot, res.Branches)
	for i, c := range caps {
		t.Logf("branch %d: key=%x stateMask=%016b treeMask=%016b extMask=%016b",
			i, c.keyHex, c.stateMask, c.treeMask, c.extMask)
	}

	// Compare encoding sizes V1 vs V2 with the captured extMask.
	var v1Total, v2Total int
	for _, c := range caps {
		v1Total += len(trie.MarshalTrieNodeDense(c.stateMask, c.treeMask, c.slotData, nil))
		v2Total += len(trie.MarshalTrieNodeDenseV2(c.stateMask, c.treeMask, c.extMask, c.slotData, nil))
	}
	t.Logf("V1 %d B / V2 %d B (saving %.1f%%)", v1Total, v2Total,
		100*(1-float64(v2Total)/float64(v1Total)))
}

// keep imports clean
var _ = bytes.Equal
var _ = fmt.Sprintf
