// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"bytes"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/params"
)

func mustRLP(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := rlp.EncodeToBytes(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// qcFixture is a committee, its strict verifier and a chain 0..top whose
// block n (n >= 2) embeds the commit QC of block n-2 (view n-2), except the
// blocks listed in bare.
type qcFixture struct {
	com    *hsCommittee
	vs     *hotstuff.ValidatorSet
	verify syncanchor.Verifier
	blocks []block.IBlock
}

func newQCFixture(t *testing.T, top uint64, bare map[uint64]bool) *qcFixture {
	t.Helper()
	com := newHSCommittee(t, 4)
	h := hotstuff.New(&params.HotStuffConfig{BaseTimeout: 60000, MaxTimeout: 120000}, nil)
	h.Authorize(com.vals[0].Address, com.keys[0])
	if err := h.InitEngine(com.vals, 1); err != nil {
		t.Fatal(err)
	}
	f := &qcFixture{com: com, vs: h.Engine().CurrentValidatorSet(), verify: hsAnchorVerifier{hotstuff.NewService(h, nil, nil, "", "")}}
	for n := uint64(0); n <= top; n++ {
		var qc *hotstuff.QuorumCertificate
		if n >= 2 && !bare[n] {
			qc = f.commitQC(t, n-2)
		}
		extra, err := hotstuff.BuildHeaderExtra(hotstuff.ViewNumber(n+1), qc)
		if err != nil {
			t.Fatal(err)
		}
		parent := comtypes.Hash{}
		if n > 0 {
			parent = f.blocks[n-1].Hash()
		}
		f.blocks = append(f.blocks, anchorBlock(parent, n, 0, extra))
	}
	return f
}

func (f *qcFixture) commitQC(t *testing.T, n uint64) *hotstuff.QuorumCertificate {
	v := hotstuff.ViewNumber(n + 1)
	target := f.blocks[n].Hash()
	return f.com.qc(t, f.vs, v, target, hotstuff.CommitSigningMessage(v, target))
}

type fakeQCSource struct {
	f    *qcFixture
	head uint64
	last *hotstuff.QuorumCertificate
}

func (s fakeQCSource) committedHead() uint64 { return s.head }
func (s fakeQCSource) canonicalHash(n uint64) comtypes.Hash {
	if n >= uint64(len(s.f.blocks)) || n > s.head {
		return comtypes.Hash{}
	}
	return s.f.blocks[n].Hash()
}
func (s fakeQCSource) headerByHash(h comtypes.Hash) *block.Header {
	for _, b := range s.f.blocks {
		if b.Hash() == h {
			return b.Header().(*block.Header)
		}
	}
	return nil
}
func (s fakeQCSource) headerAt(n uint64) *block.Header {
	if n >= uint64(len(s.f.blocks)) || n > s.head {
		return nil
	}
	return s.f.blocks[n].Header().(*block.Header)
}
func (s fakeQCSource) lastCommittedQC() *hotstuff.QuorumCertificate { return s.last }

func commitQCWire(t *testing.T, src commitQCSource, req syncwire.CommitQCRequest) []byte {
	t.Helper()
	payload, code, msg := serveCommitQC(src, req)
	if code != syncwire.CodeSuccess {
		return syncwire.AppendError(nil, code, msg)
	}
	return syncwire.AppendChunk(nil, code, nil, payload)
}

func TestCommitQCServeReadVerify(t *testing.T) {
	f := newQCFixture(t, 40, map[uint64]bool{20: true, 21: true, 22: true})
	src := fakeQCSource{f: f, head: 40}
	// Block 19: the first QC reaching it is in block 23 (QC of 21); headers 21..19.
	for _, req := range []syncwire.CommitQCRequest{
		{Number: 19, Hash: f.blocks[19].Hash()},
		{Number: 19},
	} {
		rep, err := readCommitQC(bytes.NewReader(commitQCWire(t, src, req)), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Headers) != 3 || rep.Headers[0].Number.Uint64() != 21 || rep.Headers[2].Number.Uint64() != 19 {
			t.Fatalf("headers %d", len(rep.Headers))
		}
		if r := VerifyCommitQCWith(f.verify, rep.QC); r.Verdict != syncanchor.Verified {
			t.Fatalf("verdict %v %v", r.Verdict, r.Err)
		}
	}
	// "Your highest": the persisted QC and its own header.
	src.last = f.commitQC(t, 38)
	rep, err := readCommitQC(bytes.NewReader(commitQCWire(t, src, syncwire.CommitQCRequest{})), syncwire.CommitQCRequest{})
	if err != nil || len(rep.Headers) != 1 || rep.Headers[0].Number.Uint64() != 38 {
		t.Fatalf("highest: %v", err)
	}
}

func TestCommitQCRefusals(t *testing.T) {
	f := newQCFixture(t, 30, nil)
	src := fakeQCSource{f: f, head: 25}
	for name, req := range map[string]syncwire.CommitQCRequest{
		"above head":     {Number: 26},
		"unknown hash":   {Number: 5, Hash: comtypes.HexToHash("0x77")},
		"uncommitted":    {Number: 27, Hash: f.blocks[27].Hash()},
		"top, no QC yet": {Number: 25},
	} {
		_, err := readCommitQC(bytes.NewReader(commitQCWire(t, src, req)), req)
		var ce *syncwire.CodeError
		if !errors.As(err, &ce) || ce.Code != syncwire.CodeResourceUnavailable {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := syncwire.DecodeCommitQCRequest(make([]byte, 39)); err == nil {
		t.Fatal("short request accepted")
	}
}

// An anchor more than 256 headers above the request comes without headers.
func TestCommitQCFarAnchorHasNoHeaders(t *testing.T) {
	bare := map[uint64]bool{}
	for n := uint64(3); n <= 300; n++ {
		bare[n] = true
	}
	f := newQCFixture(t, 302, bare)
	src := fakeQCSource{f: f, head: 302}
	req := syncwire.CommitQCRequest{Number: 2}
	rep, err := readCommitQC(bytes.NewReader(commitQCWire(t, src, req)), req)
	if err != nil || len(rep.Headers) != 0 || rep.QC.BlockHash != f.blocks[299].Hash() {
		t.Fatalf("far anchor: %v %d", err, len(rep.Headers))
	}
}

// Adversarial replies: a forged QC fails verification, a prepare-domain QC
// is not an anchor, a header chain that does not link or does not reach the
// request is a provable fault.
func TestCommitQCClientRejects(t *testing.T) {
	f := newQCFixture(t, 12, nil)
	req := syncwire.CommitQCRequest{Number: 8, Hash: f.blocks[8].Hash()}
	headers := func(from, to uint64) [][]byte {
		var out [][]byte
		for n := from; ; n-- {
			enc := mustRLP(t, f.blocks[n].Header())
			out = append(out, enc)
			if n == to {
				return out
			}
		}
	}
	wire := func(qc *hotstuff.QuorumCertificate, hs [][]byte) []byte {
		q, err := hotstuff.EncodeQC(qc)
		if err != nil {
			t.Fatal(err)
		}
		p, err := syncwire.CommitQCResponse{QC: q, Headers: hs}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return syncwire.AppendChunk(nil, 0, nil, p)
	}
	good := f.commitQC(t, 10)

	forged := *good
	forged.AggregateSignature = append([]byte(nil), good.AggregateSignature...)
	forged.AggregateSignature[5] ^= 0x40
	if rep, err := readCommitQC(bytes.NewReader(wire(&forged, headers(10, 8))), req); err == nil {
		if r := VerifyCommitQCWith(f.verify, rep.QC); r.Verdict == syncanchor.Verified {
			t.Fatal("forged QC verified")
		}
	}
	prep := f.com.qc(t, f.vs, 11, f.blocks[10].Hash(), hotstuff.SigningMessage(11, f.blocks[10].Hash()))
	rep, err := readCommitQC(bytes.NewReader(wire(prep, headers(10, 8))), req)
	if err != nil {
		t.Fatal(err)
	}
	if r := VerifyCommitQCWith(f.verify, rep.QC); r.Verdict != syncanchor.PrepareDomain {
		t.Fatalf("prepare QC verdict %v", r.Verdict)
	}
	broken := headers(10, 8)
	broken[1], broken[2] = broken[2], broken[1]
	if _, err := readCommitQC(bytes.NewReader(wire(good, broken)), req); !errors.Is(err, errCommitQCFault) {
		t.Fatalf("unlinked: %v", err)
	}
	if _, err := readCommitQC(bytes.NewReader(wire(good, headers(10, 9))), req); !errors.Is(err, errCommitQCFault) {
		t.Fatalf("short of the request: %v", err)
	}
	if _, err := readCommitQC(bytes.NewReader(wire(good, headers(9, 8))), req); !errors.Is(err, errCommitQCFault) {
		t.Fatalf("first header not the QC block: %v", err)
	}
}

// A verified commit_qc reply authenticates a held tail without QCs.
func TestCommitQCReleasesHeldTail(t *testing.T) {
	f := newQCFixture(t, 12, nil)
	var b anchorBook
	b.mu.Lock()
	b.setTailLocked(f.blocks[5:9])
	b.recordLinksLocked(f.blocks[5:9])
	b.mu.Unlock()
	if _, ok := b.tailReleasable(); ok {
		t.Fatal("tail releasable before any anchor")
	}
	req := syncwire.CommitQCRequest{Number: 8, Hash: f.blocks[8].Hash()}
	rep, err := readCommitQC(bytes.NewReader(commitQCWire(t, fakeQCSource{f: f, head: 12}, req)), req)
	if err != nil {
		t.Fatal(err)
	}
	if r := VerifyCommitQCWith(f.verify, rep.QC); r.Verdict != syncanchor.Verified {
		t.Fatal(r.Verdict)
	}
	b.recordHeaderLinks(rep.Headers)
	b.noteAnchor(rep.QC.BlockHash)
	if top, ok := b.tailReleasable(); !ok || top != 8 {
		t.Fatalf("tail not released: %d %v", top, ok)
	}
	if rel := b.releaseAuthenticatedTail(); len(rel) != 4 {
		t.Fatalf("released %d", len(rel))
	}
}
