package mptproof

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// TestProbe_DenseExtended_USDCSlot0 answers the architectural
// question: when walk Outcome=NoBranchAtPath at depth D, does the
// dense table have content at extended path keyNibbles[:D+1]?
//
// EMPIRICAL RESULT (2026-05-22, production D:\n42-chaindata):
//
//	depth 1..6: HIT — dense has the entire walk path.
//	depth 6 slot 11 (USDC's slot b): 33B hash a02ecfd45c1a7b39...
//	depth 7 extended path: MISS — no dense entry.
//
// Interpretation: USDC's keccak(addr) becomes the unique-prefix
// holder at depth 6 (only one contract whose keccak(addr) starts
// with [7,11,5,8,5,5,11]). From depth 7 down to 64, the trie
// collapses into a SINGLE extension node that wraps USDC's storage
// subtree root. That extension is encoded inline inside the parent
// slot's 33B hash — neither dense nor compact persists it as its
// own keyed entry.
//
// → dense-recursive descent is NOT viable for heavy-contract
// storage proofs. The under-threshold subtree truly isn't stored
// anywhere except as the parent slot's 33B hash. Workable fixes:
//
//   1. Streaming subtree builder — enumerate USDC leaves via cursor
//      but build path incrementally (still ~1M cursor ops for USDC,
//      minutes, not sub-second).
//   2. Per-contract storage tries (Ethereum-standard) — proofs
//      scoped to one contract's slots. Best long-term answer but a
//      multi-week refactor.
//   3. Persist sub-threshold branches in dense at build time — adds
//      ~30 GB to the dense table and changes hash-vs-inline rules
//      from compact's semantics. Medium effort, ugly.
func TestProbe_DenseExtended_USDCSlot0(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionChaindataDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionChaindataDir)
	}

	env, _, _, err := mpttrie.OpenUnifiedDB(productionChaindataDir)
	if err != nil {
		t.Fatalf("OpenUnifiedDB: %v", err)
	}
	defer env.Close()

	dense := mpttrie.OpenDenseShared(env, mpttrie.StoragesDenseTable)
	if has, _ := dense.Has(); !has {
		t.Skip("StoragesDense table empty")
	}

	// USDC: 0xa0b86991c6218b36c1d19d4a2e9eb0ce3606eb48
	// Walk for USDC slot 0 / slot 1 terminates at depth 6, target
	// nibble b. The composite key is keccak(usdc) || keccak(slot).
	// The walk-fired error logged prefix "070b050805050b" — that is
	// 7 nibbles stored one-per-byte (each byte's low nibble is the
	// nibble value, high nibble is 0).
	prefix7, err := hex.DecodeString("070b050805050b")
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	if len(prefix7) != 7 {
		t.Fatalf("unexpected prefix len %d", len(prefix7))
	}
	t.Logf("probing dense paths along USDC slot 0/1 walk (7-nib prefix at depth 7)")
	t.Logf("nibble bytes: %v", prefix7)

	// Probe depths 1..len(prefix7)+1. Depths 1..6 are positions the
	// walk traversed (compact branches at those depths). Depth 7 is
	// the EXTENDED path the walk tried but found nothing in compact —
	// that's the one we want to know about for dense-recursion.
	for d := 1; d <= len(prefix7)+1; d++ {
		if d > len(prefix7) {
			break
		}
		p := prefix7[:d]
		br, ok, err := dense.Get(p)
		switch {
		case err != nil:
			t.Logf("  depth %d nibbles=%v: ERROR %v", d, p, err)
		case !ok:
			t.Logf("  depth %d nibbles=%v: MISS (no dense entry)", d, p)
		default:
			t.Logf("  depth %d nibbles=%v: HIT stateMask=%016b treeMask=%016b filledSlots=%d",
				d, p, br.StateMask, br.TreeMask, countSlots(br))
		}
	}

	// The crucial extended probe: append the deepest target nibble (b)
	// to make a 7-nib extended path past the walk's deepest depth 6.
	extended := append(prefix7[:6:6], 0x0b)
	br, ok, err := dense.Get(extended)
	t.Logf("--- crucial probe ---")
	switch {
	case err != nil:
		t.Logf("extended depth 7 nibbles=%v: ERROR %v", extended, err)
	case !ok:
		t.Logf("extended depth 7 nibbles=%v: MISS (no dense at extended path)", extended)
	default:
		t.Logf("extended depth 7 nibbles=%v: HIT stateMask=%016b treeMask=%016b filledSlots=%d",
			extended, br.StateMask, br.TreeMask, countSlots(br))
	}

	// Dump the depth-6 branch's slots so we see what slot 11 (nib b)
	// actually contains. tree_mask claims it's a deeper subtree but
	// dense.Get at extended path missed — so the slot's value is the
	// authoritative source.
	t.Logf("--- depth 6 branch slot dump ---")
	br6, ok6, err6 := dense.Get(prefix7[:6])
	if err6 != nil || !ok6 {
		t.Fatalf("re-read depth 6: ok=%v err=%v", ok6, err6)
	}
	for i := 0; i < 16; i++ {
		s := br6.Slots[i]
		if s == nil {
			continue
		}
		t.Logf("  slot %2d (nib %x): len=%d kind=%s first8=%x",
			i, byte(i), len(s), slotKind(s), firstN(s, 8))
	}
}

func slotKind(s []byte) string {
	switch {
	case len(s) == 33 && s[0] == 0xa0:
		return "hash-ref(33B)"
	case len(s) >= 1 && s[0] >= 0xc0:
		return "rlp-list(inline)"
	case len(s) >= 1 && s[0] >= 0x80:
		return "rlp-string(inline)"
	default:
		return "raw"
	}
}

func firstN(s []byte, n int) []byte {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func countSlots(b mpttrie.DenseBranch) int {
	n := 0
	for i := 0; i < 16; i++ {
		if b.Slots[i] != nil {
			n++
		}
	}
	return n
}
