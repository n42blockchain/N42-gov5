// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"encoding/binary"
	gosync "sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

// The S1 gate tests run the catch-up harness with a fake consensus-side
// verifier. A fake header Extra is "N42H" ‖ view u64, optionally followed by
// 'Q' ‖ kind ‖ target hash ‖ 8 padding bytes; kind decides the verdict. Real
// BLS verification (domains, epochs, fallback) is covered in the hotstuff
// package (sync_anchor_test.go).

const (
	fakeQCVerified byte = 1
	fakeQCPrepare  byte = 2
	fakeQCBad      byte = 3
	fakeQCUnknown  byte = 4
)

func fakeExtra(view uint64, kind byte, target comtypes.Hash) []byte {
	e := make([]byte, 12, 12+42)
	copy(e, "N42H")
	binary.LittleEndian.PutUint64(e[4:], view)
	if kind == 0 {
		return e
	}
	e = append(e, 'Q', kind)
	e = append(e, target[:]...)
	return append(e, make([]byte, 8)...)
}

type fakeAnchorVerifier struct {
	mu       gosync.Mutex
	verifies int
}

func (f *fakeAnchorVerifier) NotifyBlockImported(comtypes.Hash, comtypes.Hash)            {}
func (f *fakeAnchorVerifier) NotifyBlockChecked(comtypes.Hash, comtypes.Hash)             {}
func (f *fakeAnchorVerifier) NotifyBlockRejected(comtypes.Hash)                           {}
func (f *fakeAnchorVerifier) NotifyBlockHeaderKnown(comtypes.Hash, comtypes.Hash, uint64) {}

func (f *fakeAnchorVerifier) DecodeHeaderQC(extra []byte) syncanchor.Result {
	switch {
	case len(extra) < 4 || string(extra[:4]) != "N42H":
		return syncanchor.Result{Verdict: syncanchor.NonConsensus}
	case len(extra) < 12:
		return syncanchor.Result{Verdict: syncanchor.BadExtra}
	case len(extra) == 12:
		return syncanchor.Result{Verdict: syncanchor.NoQC}
	case len(extra) != 12+42 || extra[12] != 'Q':
		return syncanchor.Result{Verdict: syncanchor.BadExtra}
	}
	var h comtypes.Hash
	copy(h[:], extra[14:46])
	return syncanchor.Result{Verdict: syncanchor.Unverified, BlockHash: h, View: binary.LittleEndian.Uint64(extra[4:12])}
}

func (f *fakeAnchorVerifier) VerifyHeaderQC(extra []byte) syncanchor.Result {
	r := f.DecodeHeaderQC(extra)
	if r.Verdict != syncanchor.Unverified {
		return r
	}
	f.mu.Lock()
	f.verifies++
	f.mu.Unlock()
	switch extra[13] {
	case fakeQCVerified:
		r.Verdict = syncanchor.Verified
	case fakeQCPrepare:
		r.Verdict = syncanchor.PrepareDomain
	case fakeQCBad:
		r.Verdict = syncanchor.BadQC
	default:
		r.Verdict = syncanchor.UnknownEpoch
	}
	return r
}

func (f *fakeAnchorVerifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verifies
}

func anchorBlock(parent comtypes.Hash, number uint64, salt byte, extra []byte) block.IBlock {
	h := &block.Header{
		ParentHash:  parent,
		UncleHash:   comtypes.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:    comtypes.BytesToAddress([]byte{salt}),
		Root:        comtypes.HexToHash("0x03"),
		TxHash:      comtypes.HexToHash("0x04"),
		ReceiptHash: comtypes.HexToHash("0x05"),
		Difficulty:  uint256.NewInt(1),
		Number:      uint256.NewInt(number),
		GasLimit:    1000,
		Time:        1700000000 + number,
		Extra:       extra,
		MixDigest:   comtypes.HexToHash("0x06"),
		BaseFee:     uint256.NewInt(1),
	}
	return block.NewBlock(h, nil)
}

// anchorExtend builds blocks base[len-1]+1..to; block n embeds a QC of kind
// kindFor(n) naming block n-2 of the same branch (0 = no QC).
func anchorExtend(base []block.IBlock, to uint64, salt byte, kindFor func(uint64) byte) []block.IBlock {
	all := append([]block.IBlock(nil), base...)
	byNum := func(n uint64) block.IBlock { return all[n-all[0].Number64().Uint64()] }
	for n := all[len(all)-1].Number64().Uint64() + 1; n <= to; n++ {
		var kind byte
		var target comtypes.Hash
		if kindFor != nil && n >= 2 {
			if k := kindFor(n); k != 0 {
				kind, target = k, byNum(n-2).Hash()
			}
		}
		all = append(all, anchorBlock(all[len(all)-1].Hash(), n, salt, fakeExtra(n, kind, target)))
	}
	return all[len(base):]
}

func allVerified(uint64) byte { return fakeQCVerified }

// anchorChain returns blocks 0..n of one branch.
func anchorChain(n uint64, kindFor func(uint64) byte) []block.IBlock {
	genesis := anchorBlock(comtypes.Hash{}, 0, 0, fakeExtra(0, 0, comtypes.Hash{}))
	return append([]block.IBlock{genesis}, anchorExtend([]block.IBlock{genesis}, n, 0, kindFor)...)
}

func newAnchorNet(t *testing.T, mode syncanchor.Mode, local []block.IBlock) (*harnessNet, *fakeAnchorVerifier) {
	t.Helper()
	t.Cleanup(syncanchor.SetForTest(mode))
	net := newHarnessNet(t, newHarnessChain(local...))
	fv := &fakeAnchorVerifier{}
	net.svc.cfg.blockImportNotifier = fv
	net.svc.claimQuorum.Store(1)
	return net, fv
}

func refused(reason string) float64 {
	return metricValue(syncAnchorWouldRefuse.WithLabelValues(reason))
}

func waitHead(t *testing.T, c *harnessChain, want uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.headNumber() != want {
		if time.Now().After(deadline) {
			t.Fatalf("head = %d, want %d", c.headNumber(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A range whose headers carry valid QCs executes up to the highest anchored
// block (one BLS verification); the top d blocks wait for the next anchor,
// and a Decide releases them.
func TestAnchorEnforceAnchoredRangeAndDecideReleasesTail(t *testing.T) {
	all := anchorChain(130, allVerified)
	net, fv := newAnchorNet(t, syncanchor.Enforce, all[:101])
	p := net.addPeer(130, harnessHonest, all...)
	okBefore := metricValue(syncAnchorOK)

	net.svc.catchUpTo(130, []peer.ID{p.id})
	if got := net.chain.headNumber(); got != 128 {
		t.Fatalf("head = %d, want 128 (130 and 129 are above the highest anchor)", got)
	}
	if got := net.svc.anchors.tailLen(); got != 2 {
		t.Fatalf("held tail = %d, want 2", got)
	}
	if got := fv.count(); got != 1 {
		t.Fatalf("QC verifications = %d, want 1 for the whole range", got)
	}
	if d := metricValue(syncAnchorOK) - okBefore; d < 28 {
		t.Fatalf("sync_anchor_ok_total grew by %v, want >= 28", d)
	}
	if net.badResponses(p) != 0 {
		t.Fatal("honest peer scored")
	}

	// A later verified CommitQC (Decide) names 130.
	net.svc.NoteCommitAnchor(all[130].Hash(), 130)
	waitHead(t, net.chain, 130)
	if got := net.svc.anchors.tailLen(); got != 0 {
		t.Fatalf("tail after release = %d", got)
	}
}

// Observe mode imports exactly as before and only counts.
func TestAnchorObserveCountsWithoutChangingImport(t *testing.T) {
	all := anchorChain(130, allVerified)
	net, _ := newAnchorNet(t, syncanchor.Observe, all[:101])
	p := net.addPeer(130, harnessHonest, all...)
	before := refused(anchorReasonUnanchored)

	net.svc.catchUpTo(130, []peer.ID{p.id})
	if got := net.chain.headNumber(); got != 130 {
		t.Fatalf("observe changed the import: head = %d, want 130", got)
	}
	if d := refused(anchorReasonUnanchored) - before; d != 2 {
		t.Fatalf("would-refuse unanchored grew by %v, want 2", d)
	}
	if net.svc.anchors.tailLen() != 0 {
		t.Fatal("observe must not hold a tail")
	}
}

// A lying peer serves a self-consistent fabricated chain.
func TestAnchorForgedChainFromLyingPeer(t *testing.T) {
	honest := anchorChain(100, allVerified)
	cases := []struct {
		name      string
		kind      func(uint64) byte
		copied    bool // embed a real canonical QC target instead
		wantScore int
	}{
		{"invalid QCs", func(uint64) byte { return fakeQCBad }, false, 1},
		{"no QCs", nil, false, 0},
		{"copied real QC", nil, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			forged := anchorExtend(honest, 160, 0xf0, tc.kind)
			if tc.copied {
				// Every forged header names a real block hash with a QC that
				// verifies; no forged block can link to it.
				real := anchorExtend(honest, 200, 0x01, allVerified)
				forged = nil
				prev := honest
				for n := uint64(101); n <= 160; n++ {
					b := anchorBlock(prev[len(prev)-1].Hash(), n, 0xf1, fakeExtra(n, fakeQCVerified, real[n-101+20].Hash()))
					forged = append(forged, b)
					prev = append(prev, b)
				}
			}
			for _, mode := range []syncanchor.Mode{syncanchor.Enforce, syncanchor.Observe} {
				net, fv := newAnchorNet(t, mode, honest)
				liar := net.addPeer(160, harnessHonest, append(append([]block.IBlock{}, honest...), forged...)...)
				badBefore := refused(anchorReasonBadQC)
				net.svc.catchUpTo(160, []peer.ID{liar.id})
				if mode == syncanchor.Enforce {
					if got := net.chain.headNumber(); got != 100 {
						t.Fatalf("enforce executed forged blocks: head = %d", got)
					}
					if net.chain.inserts != 0 {
						t.Fatalf("enforce inserted %d forged blocks", net.chain.inserts)
					}
					if got := net.badResponses(liar); got != tc.wantScore {
						t.Fatalf("liar score = %d, want %d", got, tc.wantScore)
					}
				} else {
					if got := net.chain.headNumber(); got != 160 {
						t.Fatalf("observe changed behaviour: head = %d, want 160", got)
					}
					if net.badResponses(liar) != 0 {
						t.Fatal("observe scored a peer")
					}
					if tc.kind != nil && refused(anchorReasonBadQC)-badBefore < 1 {
						t.Fatal("observe did not count the bad QC")
					}
				}
				if tc.copied && fv.count() != 0 {
					t.Fatalf("a QC naming a block outside the chain was verified %d times", fv.count())
				}
			}
		})
	}
}

// Prepare-domain and unknown-epoch QCs never anchor and are not faults.
func TestAnchorNonAnchorQCKinds(t *testing.T) {
	for _, tc := range []struct {
		kind   byte
		reason string
	}{
		{fakeQCPrepare, anchorReasonPrepareDomain},
		{fakeQCUnknown, anchorReasonUnknownEpoch},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			all := anchorChain(130, func(n uint64) byte {
				if n > 100 {
					return tc.kind
				}
				return fakeQCVerified
			})
			net, _ := newAnchorNet(t, syncanchor.Enforce, all[:101])
			p := net.addPeer(130, harnessHonest, all...)
			before := refused(tc.reason)
			net.svc.catchUpTo(130, []peer.ID{p.id})
			if got := net.chain.headNumber(); got != 100 {
				t.Fatalf("head = %d, want 100", got)
			}
			if refused(tc.reason)-before < 1 {
				t.Fatalf("%s not counted", tc.reason)
			}
			if net.badResponses(p) != 0 {
				t.Fatal("a non-anchor QC is not a provable fault")
			}
		})
	}
}

// A parent-hash break inside a response is a provable fault.
func TestAnchorBrokenLinkMidRange(t *testing.T) {
	all := anchorChain(130, allVerified)
	other := anchorExtend(all[:111], 130, 0x22, allVerified) // a sibling branch from 111
	served := append(append([]block.IBlock{}, all[:116]...), other[5:]...)
	for _, mode := range []syncanchor.Mode{syncanchor.Enforce, syncanchor.Observe} {
		net, _ := newAnchorNet(t, mode, all[:101])
		p := net.addPeer(130, harnessHonest, served...)
		before := refused(anchorReasonBrokenLink)
		net.svc.catchUpTo(130, []peer.ID{p.id})
		if d := refused(anchorReasonBrokenLink) - before; d < 1 || (mode == syncanchor.Enforce && d != 1) {
			t.Fatalf("%v: broken link counted %v times", mode, d)
		}
		if mode == syncanchor.Enforce {
			if net.chain.headNumber() != 100 || net.badResponses(p) != 1 {
				t.Fatalf("enforce: head %d score %d, want 100 and 1", net.chain.headNumber(), net.badResponses(p))
			}
		} else if net.chain.headNumber() != 115 || net.badResponses(p) != 0 {
			t.Fatalf("observe: head %d score %d, want the pre-S1 115 and 0", net.chain.headNumber(), net.badResponses(p))
		}
	}
}

func TestAnchorTailBoundEvictsHighest(t *testing.T) {
	blocks := anchorChain(100, nil)[1:]
	var b anchorBook
	b.mu.Lock()
	evicted := b.setTailLocked(blocks)
	b.mu.Unlock()
	if evicted != 36 || b.tailLen() != 64 || b.tail[0] != blocks[0] || b.tail[63] != blocks[63] {
		t.Fatalf("block bound: evicted %d, kept %d", evicted, b.tailLen())
	}
	saved := anchorTailMaxBytes
	anchorTailMaxBytes = 3 * estimateBlockBytes(blocks[0])
	defer func() { anchorTailMaxBytes = saved }()
	b.mu.Lock()
	evicted = b.setTailLocked(blocks)
	b.mu.Unlock()
	if evicted != 97 || b.tailLen() != 3 || b.tail[2] != blocks[2] {
		t.Fatalf("byte bound: evicted %d, kept %d", evicted, b.tailLen())
	}
	// A later anchor releases the authenticated prefix only.
	b.mu.Lock()
	b.recordLinksLocked(blocks[:3])
	b.mu.Unlock()
	b.noteAnchor(blocks[1].Hash())
	if rel := b.releaseAuthenticatedTail(); len(rel) != 2 || b.tailLen() != 1 {
		t.Fatalf("released %d, held %d", len(rel), b.tailLen())
	}
}

// A long stretch led by producers that embed no QC (n42-26) is anchored by
// the first QC above it: the round fetches further, then re-fetches the
// bodies the tail bound evicted.
func TestAnchorStretchWithoutQCsThenQC(t *testing.T) {
	all := anchorChain(400, func(n uint64) byte {
		if n > 300 {
			return fakeQCVerified
		}
		return 0
	})
	net, _ := newAnchorNet(t, syncanchor.Enforce, all[:101])
	p := net.addPeer(400, harnessHonest, all...)
	p.maxServe = 100 // a byte-capped server answering short prefixes
	net.svc.catchUpTo(400, []peer.ID{p.id})
	if got := net.chain.headNumber(); got != 398 {
		t.Fatalf("head = %d, want 398", got)
	}
	if net.badResponses(p) != 0 {
		t.Fatal("a QC-less stretch is not a fault")
	}
}

// Off is the pre-S1 behaviour: forged blocks execute and nothing is verified.
func TestAnchorOffModeIsUnchanged(t *testing.T) {
	honest := anchorChain(100, allVerified)
	forged := anchorExtend(honest, 160, 0xf0, func(uint64) byte { return fakeQCBad })
	net, fv := newAnchorNet(t, syncanchor.Off, honest)
	liar := net.addPeer(160, harnessHonest, append(append([]block.IBlock{}, honest...), forged...)...)
	before := refused(anchorReasonBadQC)
	net.svc.catchUpTo(160, []peer.ID{liar.id})
	if net.chain.headNumber() != 160 || fv.count() != 0 || refused(anchorReasonBadQC) != before {
		t.Fatalf("off mode: head %d, verifications %d", net.chain.headNumber(), fv.count())
	}
	net.svc.NoteCommitAnchor(forged[10].Hash(), 111)
	if net.svc.anchors.isAuth(forged[10].Hash()) {
		t.Fatal("off mode recorded an anchor")
	}
}

// Without a consensus verifier (not a HotStuff chain) the gate is inactive
// in every mode.
func TestAnchorInactiveWithoutVerifier(t *testing.T) {
	defer syncanchor.SetForTest(syncanchor.Enforce)()
	all := harnessBase(130)
	net := newHarnessNet(t, newHarnessChain(all[:101]...))
	p := net.addPeer(130, harnessHonest, all...)
	net.svc.catchUpTo(130, []peer.ID{p.id})
	if net.chain.headNumber() != 130 {
		t.Fatalf("head = %d, want 130", net.chain.headNumber())
	}
}

// The stage-A scenario (restarted validator of a 7-node network, one extra
// peer lying about its height) in enforce mode, with the liar serving a
// self-consistent fabricated chain without QCs: no fabricated block is ever
// executed, the honest chain is caught up to its highest anchor, and the
// last d blocks follow the next Decide.
func TestCatchUpScenarioEnforceRestartWithForgingPeer(t *testing.T) {
	all := anchorChain(240, allVerified)
	net, _ := newAnchorNet(t, syncanchor.Enforce, all[:101])
	validators := make([]*harnessPeer, 0, 6)
	for i := 0; i < 6; i++ {
		validators = append(validators, net.addPeer(230, harnessHonest, all[:236]...))
	}
	forged := anchorExtend(all[:101], 1400, 0xf0, nil)
	liar := net.addPeer(1_000_000_000, harnessHonest, append(append([]block.IBlock{}, all[:101]...), forged...)...)

	if got := net.svc.HeightBehindQuorum(3); got != 130 {
		t.Fatalf("gate gap = %d, want 130", got)
	}
	net.drain(10)
	if got := net.chain.headNumber(); got != 233 {
		t.Fatalf("head = %d, want 233 (235 is stored; 234-235 wait for an anchor)", got)
	}
	net.chain.mu.Lock()
	for _, f := range forged {
		if net.chain.appliedLocked(f.Hash()) {
			net.chain.mu.Unlock()
			t.Fatalf("fabricated block %d executed", f.Number64().Uint64())
		}
	}
	net.chain.mu.Unlock()
	if liar.askedCount() == 0 {
		t.Fatal("the liar was never asked; the test exercises nothing")
	}
	for _, v := range validators {
		if net.badResponses(v) != 0 {
			t.Fatal("honest validator penalised")
		}
	}

	// The network commits 235 (a Decide names it).
	net.svc.NoteCommitAnchor(all[235].Hash(), 235)
	deadline := time.Now().Add(5 * time.Second)
	for net.chain.headNumber() != 235 && time.Now().Before(deadline) {
		net.svc.CatchUp() // the 8 s tick
		time.Sleep(10 * time.Millisecond)
	}
	if got := net.chain.headNumber(); got != 235 {
		t.Fatalf("head after the Decide = %d, want 235", got)
	}
}

// metricValue reads a counter (testutil would need a new go.mod requirement).
func metricValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return m.GetCounter().GetValue()
}
