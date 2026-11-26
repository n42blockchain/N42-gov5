// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package v11

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/lib/rlp"
)

// v11Chain builds blocks 0..n, parent-linked, with deterministic headers.
func v11Chain(n uint64) []*block.Block {
	out := make([]*block.Block, 0, n+1)
	parent := comtypes.Hash{}
	for i := uint64(0); i <= n; i++ {
		h := &block.Header{
			ParentHash:  parent,
			UncleHash:   comtypes.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
			Coinbase:    comtypes.HexToAddress("0x02"),
			Root:        comtypes.BytesToHash([]byte{byte(i), 0x03}),
			TxHash:      comtypes.HexToHash("0x04"),
			ReceiptHash: comtypes.HexToHash("0x05"),
			Difficulty:  uint256.NewInt(0),
			Number:      uint256.NewInt(i),
			GasLimit:    480000000,
			Time:        1700000000 + i,
			Extra:       []byte("N42H"),
			BaseFee:     uint256.NewInt(1000000000),
		}
		b := block.NewBlock(h, nil).(*block.Block)
		parent = b.Hash()
		out = append(out, b)
	}
	return out
}

type fakeRangeSource struct {
	blocks   []*block.Block
	head     uint64
	earliest uint64
}

func (f *fakeRangeSource) CommittedHead() uint64 { return f.head }
func (f *fakeRangeSource) EarliestBody() uint64  { return f.earliest }
func (f *fakeRangeSource) BlockAt(n uint64) block.IBlock {
	if n >= uint64(len(f.blocks)) {
		return nil
	}
	return f.blocks[n]
}
func (f *fakeRangeSource) HeaderAt(n uint64) *block.Header {
	if n >= uint64(len(f.blocks)) {
		return nil
	}
	return f.blocks[n].Header().(*block.Header)
}
func (f *fakeRangeSource) Digest() [4]byte { return [4]byte{0xde, 0xad, 0xbe, 0xef} }

func serveV2(src RangeSource, req syncwire.RangeRequest) ([]byte, syncwire.RangeEnd) {
	var buf bytes.Buffer
	end := ServeRangeV2(src, req, func(b []byte) bool { buf.Write(b); return true })
	return buf.Bytes(), end
}

func blockLen(t *testing.T, b *block.Block) uint64 {
	enc, err := rlp.EncodeToBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	return uint64(len(enc))
}

func TestRangeV2ServeAndRead(t *testing.T) {
	src := &fakeRangeSource{blocks: v11Chain(20), head: 15}
	one := blockLen(t, src.blocks[1])
	for _, tc := range []struct {
		name   string
		req    syncwire.RangeRequest
		served int
		reason byte
	}{
		{"complete", syncwire.RangeRequest{Start: 1, Count: 5}, 5, syncwire.ReasonComplete},
		{"to committed head", syncwire.RangeRequest{Start: 10, Count: 10}, 6, syncwire.ReasonCanonicalEnd},
		{"ahead of head", syncwire.RangeRequest{Start: 16, Count: 3}, 0, syncwire.ReasonCanonicalEnd},
		{"byte budget", syncwire.RangeRequest{Start: 1, Count: 10, MaxBytes: 2*one + one/2}, 2, syncwire.ReasonByteBudget},
		{"budget below one block still serves one", syncwire.RangeRequest{Start: 1, Count: 10, MaxBytes: 1}, 1, syncwire.ReasonByteBudget},
		{"headers only", syncwire.RangeRequest{Start: 3, Count: 4, Flags: syncwire.FlagHeadersOnly}, 4, syncwire.ReasonComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, end := serveV2(src, tc.req)
			if end.Reason != tc.reason || end.Next != tc.req.Start+uint64(tc.served) {
				t.Fatalf("END %+v, want %d served reason %d", end, tc.served, tc.reason)
			}
			res, err := ReadRangeV2(bytes.NewReader(wire), tc.req, nil)
			if err != nil {
				t.Fatal(err)
			}
			got := len(res.Blocks) + len(res.Headers)
			if got != tc.served || res.End == nil || *res.End != end {
				t.Fatalf("read %d, END %+v", got, res.End)
			}
		})
	}
}

func TestRangeV2BelowEarliestIsNotAvailable(t *testing.T) {
	src := &fakeRangeSource{blocks: v11Chain(20), head: 20, earliest: 10}
	wire, end := serveV2(src, syncwire.RangeRequest{Start: 5, Count: 3})
	if end.Reason != syncwire.ReasonNotAvailable || end.Next != 5 {
		t.Fatalf("END %+v", end)
	}
	if _, err := ReadRangeV2(bytes.NewReader(wire), syncwire.RangeRequest{Start: 5, Count: 3}, nil); err != nil {
		t.Fatal(err)
	}
	// Headers below the earliest body are still served.
	_, end = serveV2(src, syncwire.RangeRequest{Start: 5, Count: 3, Flags: syncwire.FlagHeadersOnly})
	if end.Reason != syncwire.ReasonComplete {
		t.Fatalf("headers END %+v", end)
	}
}

// A canonical hole (a block that does not link to the previous one) ends the
// reply with NOT_AVAILABLE at the hole.
func TestRangeV2StopsAtCanonicalBreak(t *testing.T) {
	blocks := v11Chain(10)
	h := *blocks[6].Header().(*block.Header)
	h.ParentHash = comtypes.HexToHash("0x99")
	blocks[6] = block.NewBlock(&h, nil).(*block.Block)
	src := &fakeRangeSource{blocks: blocks, head: 10}
	_, end := serveV2(src, syncwire.RangeRequest{Start: 3, Count: 6})
	if end.Next != 6 || end.Reason != syncwire.ReasonNotAvailable {
		t.Fatalf("END %+v", end)
	}
}

// Adversarial replies: an END that lies, more chunks than requested, a chunk
// that does not link, and a missing END (soft: prefix kept, no fault).
func TestRangeV2ClientFaults(t *testing.T) {
	src := &fakeRangeSource{blocks: v11Chain(20), head: 20}
	req := syncwire.RangeRequest{Start: 1, Count: 3}
	digest := src.Digest()
	chunk := func(n uint64) []byte {
		enc, _ := rlp.EncodeToBytes(src.blocks[n])
		return syncwire.AppendChunk(nil, syncwire.CodeSuccess, &digest, enc)
	}
	end := func(e syncwire.RangeEnd) []byte { return syncwire.AppendChunk(nil, syncwire.CodeEnd, nil, e.Encode()) }
	good, _ := serveV2(src, req)
	isFault := func(err error) bool { return errors.Is(err, ErrRangeFault) }

	// END claims more bytes than were sent.
	lie := append(append(append(chunk(1), chunk(2)...), chunk(3)...), end(syncwire.RangeEnd{Next: 4, ServedBytes: 1, Reason: 0})...)
	if _, err := ReadRangeV2(bytes.NewReader(lie), req, nil); !isFault(err) {
		t.Fatalf("lying END: %v", err)
	}
	// Four chunks for a count of three.
	extra := append(append(append(chunk(1), chunk(2)...), chunk(3)...), chunk(4)...)
	if _, err := ReadRangeV2(bytes.NewReader(extra), req, nil); !isFault(err) {
		t.Fatalf("extra chunk: %v", err)
	}
	// Block 3 then block 2.
	swapped := append(append(chunk(1), chunk(3)...), chunk(2)...)
	if _, err := ReadRangeV2(bytes.NewReader(swapped), req, nil); !isFault(err) {
		t.Fatalf("non-contiguous: %v", err)
	}
	// Missing END after two blocks: kept, no error.
	res, err := ReadRangeV2(bytes.NewReader(append(chunk(1), chunk(2)...)), req, nil)
	if err != nil || len(res.Blocks) != 2 || res.End != nil {
		t.Fatalf("missing END: %v %d", err, len(res.Blocks))
	}
	// An error chunk before any block is returned as a code error.
	_, err = ReadRangeV2(bytes.NewReader(syncwire.AppendError(nil, syncwire.CodeRateLimited, "1000")), req, nil)
	var ce *syncwire.CodeError
	if !errors.As(err, &ce) || ce.Code != syncwire.CodeRateLimited || ce.Msg != "1000" {
		t.Fatalf("rate limited: %v", err)
	}
	if _, err := ReadRangeV2(bytes.NewReader(good), req, nil); err != nil {
		t.Fatal(err)
	}
}
