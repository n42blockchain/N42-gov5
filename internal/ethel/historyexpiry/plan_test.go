package historyexpiry

import "testing"

func TestComputeWithinWindow(t *testing.T) {
	// Tip below the window → nothing cold.
	p := Compute(1_000_000, DefaultWindowBlocks, SegSize)
	if p.ColdUntilSeg != 0 || p.HotFromBlock != 0 {
		t.Errorf("within window: want all-hot, got ColdUntilSeg=%d HotFromBlock=%d", p.ColdUntilSeg, p.HotFromBlock)
	}
	if len(p.ColdSegs(0, 1000)) != 0 {
		t.Error("within window: expected no cold segments")
	}
}

func TestComputeBoundaryAlignsDown(t *testing.T) {
	const tip = 25_000_000
	const window = 2_629_800
	p := Compute(tip, window, SegSize)
	wantHotFrom := uint64(tip - window) // 22,370,200
	if p.HotFromBlock != wantHotFrom {
		t.Fatalf("HotFromBlock=%d want %d", p.HotFromBlock, wantHotFrom)
	}
	wantHotSeg := wantHotFrom / SegSize // floor → boundary segment stays hot
	if p.HotFromSeg != wantHotSeg || p.ColdUntilSeg != wantHotSeg {
		t.Fatalf("HotFromSeg=%d ColdUntilSeg=%d want %d", p.HotFromSeg, p.ColdUntilSeg, wantHotSeg)
	}
	// The boundary block itself must remain hot (>= window retained).
	if p.IsCold(wantHotFrom) {
		t.Error("HotFromBlock must be hot")
	}
	// The block just below the aligned segment boundary is cold.
	if !p.IsCold(p.HotFromSeg*SegSize - 1) {
		t.Error("block below the hot segment must be cold")
	}
	// At least `window` blocks kept hot (alignment keeps a few extra).
	keptHot := tip - p.HotFromSeg*SegSize
	if keptHot < window {
		t.Errorf("kept hot %d < window %d", keptHot, window)
	}
}

func TestColdSegsRespectsAvailableRange(t *testing.T) {
	// Post-merge-only store: segments start at the merge segment, not 0.
	const tip = 25_000_000
	p := Compute(tip, 2_629_800, SegSize)
	const mergeSeg = 1896
	cold := p.ColdSegs(mergeSeg, tip/SegSize)
	if len(cold) == 0 {
		t.Fatal("expected cold segments for a post-merge store at tip 25M")
	}
	// All returned segments are < ColdUntilSeg and >= mergeSeg, contiguous.
	for i, s := range cold {
		if s.Seg < mergeSeg || s.Seg >= p.ColdUntilSeg {
			t.Errorf("seg %d out of [%d,%d)", s.Seg, mergeSeg, p.ColdUntilSeg)
		}
		if s.FirstBlock != s.Seg*SegSize || s.LastBlock != s.Seg*SegSize+SegSize-1 {
			t.Errorf("seg %d bad block range %d..%d", s.Seg, s.FirstBlock, s.LastBlock)
		}
		if i > 0 && s.Seg != cold[i-1].Seg+1 {
			t.Errorf("non-contiguous cold segs at %d", i)
		}
	}
	if first := cold[0].Seg; first != mergeSeg {
		t.Errorf("cold should start at mergeSeg %d, got %d", mergeSeg, first)
	}
	if last := cold[len(cold)-1].Seg; last != p.ColdUntilSeg-1 {
		t.Errorf("cold should end at ColdUntilSeg-1 %d, got %d", p.ColdUntilSeg-1, last)
	}
}
