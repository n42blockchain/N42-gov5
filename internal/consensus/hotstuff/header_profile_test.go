// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/params"
)

const (
	p1TestGasLimit  = 480_000_000
	p1TestGenesisT0 = 1795564800
	p1TestSlot      = 8
)

// p1Fixture is a profile-1 chain with four deterministic validators (the
// keys of the profile-1 test genesis), the AM22 values and a genesis header.
type p1Fixture struct {
	t       *testing.T
	cfg     *params.ChainConfig
	hs      *HotStuff
	chain   *mockChainHeaderReader
	sks     []blscommon.SecretKey
	addrs   []types.Address
	genesis *block.Header
}

func p1TestKeys(t *testing.T) ([]blscommon.SecretKey, []params.HotStuffValidatorConfig, []types.Address) {
	t.Helper()
	var sks []blscommon.SecretKey
	var vcs []params.HotStuffValidatorConfig
	var addrs []types.Address
	for i := 0; i < 4; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("n42-profile1-test-validator-%d", i)))
		seed[0] = 0
		sk, err := bls.SecretKeyFromBytes(seed[:])
		if err != nil {
			t.Fatal(err)
		}
		pk := sk.PublicKey().Marshal()
		addr := types.BytesToAddress(crypto.Keccak256(pk)[12:])
		sks = append(sks, sk)
		addrs = append(addrs, addr)
		vcs = append(vcs, params.HotStuffValidatorConfig{Address: addr.Hex(), BLSKey: hexutil.Encode(pk)})
	}
	return sks, vcs, addrs
}

func newP1Fixture(t *testing.T) *p1Fixture {
	t.Helper()
	sks, vcs, addrs := p1TestKeys(t)
	faucet := types.HexToAddress("0x42e9819036f61bF665D5f727E8C03121f12f586e")
	cfg := &params.ChainConfig{
		ChainID:       big.NewInt(1144),
		N42Profile:    params.N42Profile1,
		LondonBlock:   big.NewInt(0),
		ShanghaiTime:  big.NewInt(0),
		CancunTime:    big.NewInt(0),
		PragueTime:    big.NewInt(0),
		AmsterdamTime: big.NewInt(0),
		HotStuff: &params.HotStuffConfig{
			Validators: vcs, LeaderTenure: 1, BaseTimeoutMs: 24000, MaxTimeoutMs: 96000,
			BlockIntervalMs: 8000, EpochLength: 10800, DeferredExecutionDepth: 1,
			GasLimit: p1TestGasLimit, MaxBlockBytes: 64 << 20, DevFaucetAddress: &faucet,
		},
	}
	zero := uint64(0)
	rq, requests := hash.EmptyRootHash, Profile1RequestsHash
	genesis := &block.Header{
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(0), GasLimit: p1TestGasLimit,
		Time: p1TestGenesisT0, BaseFee: uint256.NewInt(params.InitialBaseFee),
		TxHash: hash.EmptyRootHash, ReceiptHash: hash.EmptyRootHash,
		WithdrawalsHash: &rq, BlobGasUsed: &zero, ExcessBlobGas: &zero,
		ParentBeaconRoot: new(types.Hash), RequestsHash: &requests, SlotNumber: &zero,
		MobileRegistryRoot: new(types.Hash),
	}
	chain := newMockChainReader()
	chain.config = cfg
	chain.headers[0] = genesis
	hs := New(cfg.HotStuff, cfg)
	return &p1Fixture{t: t, cfg: cfg, hs: hs, chain: chain, sks: sks, addrs: addrs, genesis: genesis}
}

// leader is the validator index leading view (tenure 1).
func (f *p1Fixture) leader(view ViewNumber) int { return int(view % uint64(len(f.sks))) }

// child builds the unsealed profile-1 header extending parent in view with
// the optional embedded commit QC.
func (f *p1Fixture) child(parent *block.Header, view ViewNumber, qc *QuorumCertificate) *block.Header {
	f.t.Helper()
	extra, err := buildHeaderExtra(view, qc)
	if err != nil {
		f.t.Fatal(err)
	}
	n := parent.Number.Uint64() + 1
	zero := uint64(0)
	slot := n
	rq := Profile1RequestsHash
	baseFee, _ := uint256.FromBig(misc.CalcBaseFee(f.cfg, parent))
	reward := uint256.NewInt(1e18)
	rewards := block.Rewards{{Address: f.addrs[f.leader(view)], Amount: reward}}
	wh := hash.DeriveSha(rewards)
	return &block.Header{
		ParentHash: parent.Hash(), Coinbase: f.addrs[f.leader(view)], Root: parent.Root,
		TxHash: hash.EmptyRootHash, ReceiptHash: parent.ReceiptHash,
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(n), GasLimit: p1TestGasLimit,
		GasUsed: parent.GasUsed, Time: parent.Time + p1TestSlot, Extra: extra, BaseFee: baseFee,
		WithdrawalsHash: &wh, BlobGasUsed: &zero, ExcessBlobGas: &zero,
		ParentBeaconRoot: new(types.Hash), RequestsHash: &rq, SlotNumber: &slot,
		MobileRegistryRoot: new(types.Hash),
	}
}

// sealBy fills the seal area of h with validator i's signature over sealHash.
func (f *p1Fixture) sealBy(h *block.Header, i int) *block.Header {
	h.ResetHashCache()
	digest := sealHash(h)
	sig := f.sks[i].Sign(digest[:]).Marshal()
	copy(h.Extra[len(h.Extra)-extraSealLen:], sig)
	h.ResetHashCache()
	return h
}

// seal seals h by the leader of its view.
func (f *p1Fixture) seal(h *block.Header) *block.Header {
	view, err := ExtractViewFromExtra(h.Extra)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.sealBy(h, f.leader(view))
}

// commitQC is a quorum certificate over (view, blockHash) by signers, in
// the H2-v4 commit domain (commit=true) or the vote domain.
func (f *p1Fixture) commitQC(view ViewNumber, blockHash types.Hash, signers []int, commit bool) *QuorumCertificate {
	id := H2V4ChainIdentity{ChainID: 1144, GenesisHash: f.genesis.Hash()}
	msg := H2V4VoteSigningMessage(id, view, blockHash)
	if commit {
		msg = H2V4CommitSigningMessage(id, view, blockHash, types.Hash{})
	}
	bitmap := make([]bool, len(f.sks))
	var sigs []blscommon.Signature
	for _, i := range signers {
		bitmap[i] = true
		sigs = append(sigs, f.sks[i].Sign(msg))
	}
	return &QuorumCertificate{View: view, BlockHash: blockHash,
		AggregateSignature: bls.AggregateSignatures(sigs).Marshal(), Signers: bitmap}
}

// chainOf stores headers 1..n (sealed, valid) and returns them.
func (f *p1Fixture) chainOf(n int) []*block.Header {
	out := []*block.Header{f.genesis}
	for i := 1; i <= n; i++ {
		h := f.seal(f.child(out[i-1], ViewNumber(i), nil))
		f.chain.headers[uint64(i)] = h
		out = append(out, h)
	}
	return out
}

func TestProfile1HeaderValid(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(2)
	// Block 3 embeds the CommitQC of block 2 (view 2).
	qc := f.commitQC(2, hs[2].Hash(), []int{0, 1, 2}, true)
	h3 := f.seal(f.child(hs[2], 3, qc))
	if err := f.hs.VerifyHeader(f.chain, h3, true); err != nil {
		t.Fatalf("valid profile-1 header refused: %v", err)
	}
	// A header that skips slots (after a timeout) is valid: any later slot
	// of the genesis grid (B4, AM27).
	h3skip := f.child(hs[2], 3, nil)
	h3skip.Time = hs[2].Time + 3*p1TestSlot
	if err := f.hs.VerifyHeader(f.chain, f.seal(h3skip), true); err != nil {
		t.Fatalf("header three slots after its parent refused: %v", err)
	}
	// The vote-time check accepts time == floor(now)+2.
	now := time.Unix(int64(h3.Time)-2, 999_000_000)
	if err := f.hs.StaticHeaderCheck(f.chain, h3, now); err != nil {
		t.Fatalf("StaticHeaderCheck at the bound: %v", err)
	}
}

func TestProfile1HeaderRefusals(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(2)
	parent := hs[2]
	sibling := f.seal(f.child(hs[0], 1, nil))
	sibling.Time++ // a different block at height 1
	f.sealBy(sibling, f.leader(1))
	f.chain.headers[1001] = sibling

	h256 := func(s string) *types.Hash { v := types.HexToHash(s); return &v }
	cases := []struct {
		name   string
		mutate func(h *block.Header) *block.Header // returns the header to verify
		want   error
	}{
		{"B1 extra longer than 4096", func(h *block.Header) *block.Header {
			h.Extra = append(append(append([]byte{}, h.Extra[:extraMinLen]...), make([]byte, 4096)...), h.Extra[extraMinLen:]...)
			return f.seal(h)
		}, ErrProfile1Extra},
		{"B1 extra without seal", func(h *block.Header) *block.Header {
			h.Extra = h.Extra[:extraMinLen]
			return h
		}, ErrProfile1Extra},
		{"B1 bad magic", func(h *block.Header) *block.Header {
			h = f.seal(h)
			h.Extra[0] = 'X'
			h.ResetHashCache()
			return h
		}, ErrProfile1Extra},
		{"B1 encoded empty QC", func(h *block.Header) *block.Header {
			empty, _ := encodeQC(&QuorumCertificate{})
			h.Extra = append(append(append([]byte{}, h.Extra[:extraMinLen]...), empty...), make([]byte, extraSealLen)...)
			return f.seal(h)
		}, ErrProfile1Extra},
		{"B1 QC view not below header view", func(h *block.Header) *block.Header {
			return f.seal(f.child(parent, 3, f.commitQC(3, parent.Hash(), []int{0, 1, 2}, true)))
		}, ErrProfile1QC},
		{"E13 QC in the vote domain", func(h *block.Header) *block.Header {
			return f.seal(f.child(parent, 3, f.commitQC(2, parent.Hash(), []int{0, 1, 2}, false)))
		}, ErrProfile1QC},
		{"E13 QC below quorum", func(h *block.Header) *block.Header {
			return f.seal(f.child(parent, 3, f.commitQC(2, parent.Hash(), []int{0, 1}, true)))
		}, ErrProfile1QC},
		{"B1 QC names a non-ancestor", func(h *block.Header) *block.Header {
			return f.seal(f.child(parent, 3, f.commitQC(1, sibling.Hash(), []int{0, 1, 2}, true)))
		}, ErrProfile1QC},
		{"B2 seal by a non-leader", func(h *block.Header) *block.Header {
			return f.sealBy(h, (f.leader(3)+1)%4)
		}, ErrProfile1Seal},
		{"B2 seal bytes not a signature", func(h *block.Header) *block.Header {
			for i := len(h.Extra) - extraSealLen; i < len(h.Extra); i++ {
				h.Extra[i] = 0xff
			}
			h.ResetHashCache()
			return h
		}, ErrProfile1Seal},
		{"B2 header changed after sealing", func(h *block.Header) *block.Header {
			h = f.seal(h)
			h.GasUsed++
			h.ResetHashCache()
			return h
		}, ErrProfile1Seal},
		{"B3 coinbase not the leader", func(h *block.Header) *block.Header {
			h.Coinbase = f.addrs[(f.leader(3)+1)%4]
			return f.seal(h)
		}, ErrProfile1Proposer},
		{"B3 difficulty 1", func(h *block.Header) *block.Header {
			h.Difficulty = uint256.NewInt(1)
			return f.seal(h)
		}, ErrProfile1Difficulty},
		{"B3 nonce 1", func(h *block.Header) *block.Header {
			h.Nonce = block.EncodeNonce(1)
			return f.seal(h)
		}, ErrProfile1Nonce},
		{"B3 mixHash non-zero", func(h *block.Header) *block.Header {
			h.MixDigest = types.HexToHash("0x01")
			return f.seal(h)
		}, ErrProfile1MixHash},
		{"B3 ommersHash = empty-list hash", func(h *block.Header) *block.Header {
			h.UncleHash = hash.EmptyUncleHash
			return f.seal(h)
		}, ErrProfile1OmmersHash},
		{"B4 timestamp before parent (on the grid)", func(h *block.Header) *block.Header {
			h.Time = parent.Time - p1TestSlot
			return f.seal(h)
		}, ErrProfile1TimeBeforeParent},
		{"B4 timestamp equal to the parent's (AM27)", func(h *block.Header) *block.Header {
			h.Time = parent.Time
			return f.seal(h)
		}, ErrProfile1TimeBeforeParent},
		{"B4 timestamp off the grid, one second late", func(h *block.Header) *block.Header {
			h.Time = parent.Time + p1TestSlot + 1
			return f.seal(h)
		}, ErrProfile1TimeOffGrid},
		{"B4 timestamp off the grid, between parent and next slot", func(h *block.Header) *block.Header {
			h.Time = parent.Time + 1
			return f.seal(h)
		}, ErrProfile1TimeOffGrid},
		{"B4 timestamp before the genesis", func(h *block.Header) *block.Header {
			h.Time = p1TestGenesisT0 - p1TestSlot
			return f.seal(h)
		}, ErrProfile1TimeOffGrid},
		{"B6 withdrawalsRoot absent", func(h *block.Header) *block.Header {
			h.WithdrawalsHash = nil
			return f.seal(h)
		}, ErrProfile1WithdrawalsRoot},
		{"B8 requestsHash absent", func(h *block.Header) *block.Header {
			h.RequestsHash = nil
			return f.seal(h)
		}, ErrProfile1RequestsHash},
		{"B9 blobGasUsed 1", func(h *block.Header) *block.Header {
			one := uint64(1)
			h.BlobGasUsed = &one
			return f.seal(h)
		}, ErrProfile1BlobGas},
		{"B9 excessBlobGas 1", func(h *block.Header) *block.Header {
			one := uint64(1)
			h.ExcessBlobGas = &one
			return f.seal(h)
		}, ErrProfile1BlobGas},
		{"B12 element 22 present", func(h *block.Header) *block.Header {
			h.BlockAccessListHash = h256(hash.NilHash.Hex())
			return f.seal(h)
		}, ErrProfile1Element22},
		{"B14 baseFee off by one", func(h *block.Header) *block.Header {
			h.BaseFee = new(uint256.Int).AddUint64(h.BaseFee, 1)
			return f.seal(h)
		}, ErrProfile1BaseFee},
		{"A23 gasLimit one above hotstuff.gasLimit", func(h *block.Header) *block.Header {
			h.GasLimit++
			return f.seal(h)
		}, ErrProfile1GasLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.mutate(f.child(parent, 3, nil))
			err := f.hs.VerifyHeader(f.chain, h, true)
			if !errors.Is(err, tc.want) {
				t.Fatalf("VerifyHeader = %v, want %v", err, tc.want)
			}
		})
	}
}

// requestsHash is a deferred field (AM25): the header check requires it to be
// present, and its value -- the parent's executed EIP-7685 requests hash, not
// necessarily sha256("") -- is checked against the parent's execution, not
// here.
func TestProfile1HeaderRequestsHashValueIsNotStatic(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(2)
	h := f.child(hs[2], 3, nil)
	rq := types.HexToHash("0x1d6c4ad8fdd6a87a1d03fb7d1c4c1ad8b1a6c7e4a3e5d1c0a6c3b2a1f0e9d8c7")
	h.RequestsHash = &rq
	if err := f.hs.VerifyHeader(f.chain, f.seal(h), true); err != nil {
		t.Fatalf("VerifyHeader of a non-empty requestsHash = %v, want nil", err)
	}
}

func TestProfile1HeaderClockOnlyAtVoteTime(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(1)
	h := f.seal(f.child(hs[1], 2, nil))
	now := time.Unix(int64(h.Time)-3, 0)
	if err := f.hs.StaticHeaderCheck(f.chain, h, now); !errors.Is(err, ErrProfile1TimeFuture) {
		t.Fatalf("StaticHeaderCheck 3 s ahead = %v, want %v", err, ErrProfile1TimeFuture)
	}
	// Import of the same header does not consult the clock (B4).
	if err := f.hs.VerifyHeader(f.chain, h, true); err != nil {
		t.Fatalf("VerifyHeader of a future-stamped header: %v", err)
	}
	// The grid and later-slot rules apply before voting too.
	for _, bad := range []struct {
		t    uint64
		want error
	}{{hs[1].Time + 1, ErrProfile1TimeOffGrid}, {hs[1].Time, ErrProfile1TimeBeforeParent}} {
		hb := f.child(hs[1], 2, nil)
		hb.Time = bad.t
		hb = f.seal(hb)
		if err := f.hs.StaticHeaderCheck(f.chain, hb, time.Unix(int64(hb.Time)+60, 0)); !errors.Is(err, bad.want) {
			t.Fatalf("StaticHeaderCheck time %d = %v, want %v", bad.t, err, bad.want)
		}
	}
}

// Block 1's parent is the genesis (time G): block 1 is stamped G + s*I with
// s >= 1, and a block 1 at G is refused (B4, AM27).
func TestProfile1Block1Time(t *testing.T) {
	f := newP1Fixture(t)
	h := f.child(f.genesis, 1, nil)
	h.Time = p1TestGenesisT0
	if err := f.hs.VerifyHeader(f.chain, f.seal(h), true); !errors.Is(err, ErrProfile1TimeBeforeParent) {
		t.Fatalf("block 1 at G = %v, want %v", err, ErrProfile1TimeBeforeParent)
	}
	f.hs.now = func() time.Time { return time.Unix(p1TestGenesisT0-30, 0) }
	p := &block.Header{ParentHash: f.genesis.Hash(), Number: uint256.NewInt(1)}
	if err := f.hs.Prepare(f.chain, p); err != nil {
		t.Fatal(err)
	}
	if p.Time != p1TestGenesisT0+p1TestSlot {
		t.Fatalf("block 1 stamped before G: %d, want G + I = %d", p.Time, p1TestGenesisT0+p1TestSlot)
	}
}

func TestProfile1PrepareStamps(t *testing.T) {
	f := newP1Fixture(t)
	parent := f.chainOf(1)[1]
	clock := time.Unix(int64(parent.Time)+5, 700_000_000)
	f.hs.now = func() time.Time { return clock }

	prepareOn := func(f *p1Fixture, p *block.Header, minerTime uint64) *block.Header {
		h := &block.Header{ParentHash: p.Hash(), Number: uint256.NewInt(p.Number.Uint64() + 1), GasLimit: 30_000_000, Time: minerTime}
		if err := f.hs.Prepare(f.chain, h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	// On-time parent: the next slot, parent.time + I (A17, AM27).
	h := prepareOn(f, parent, 0)
	if h.Time != parent.Time+p1TestSlot {
		t.Fatalf("stamp = %d, want parent.time + I = %d", h.Time, parent.Time+p1TestSlot)
	}
	if h.GasLimit != p1TestGasLimit {
		t.Fatalf("gasLimit = %d, want hotstuff.gasLimit %d (A23)", h.GasLimit, p1TestGasLimit)
	}
	// A rebuild of the same view on the same parent re-stamps the same time,
	// even past the slot boundary.
	clock = clock.Add(3 * time.Second)
	if again := prepareOn(f, parent, 0); again.Time != h.Time {
		t.Fatalf("rebuild stamped %d, first stamp %d (one proposal per view)", again.Time, h.Time)
	}

	cases := []struct {
		name      string
		now       time.Time
		minerTime uint64
		wantSlots uint64 // stamp = parent.time + wantSlots*I
	}{
		{"clock behind the parent", time.Unix(int64(parent.Time)-10, 0), 0, 1},
		{"late parent: view entered mid next slot", time.Unix(int64(parent.Time)+11, 0), 0, 2},
		{"after a TC three slots later, mid-slot", time.Unix(int64(parent.Time)+3*p1TestSlot+1, 0), 0, 4},
		{"exactly on a boundary", time.Unix(int64(parent.Time)+2*p1TestSlot, 0), 0, 2},
		{"miner slot chosen before the wait is kept past the boundary", time.Unix(int64(parent.Time)+p1TestSlot, 200_000_000), parent.Time + p1TestSlot, 1},
		{"off-grid miner time is ignored", time.Unix(int64(parent.Time)+3, 0), parent.Time + 3, 1},
		{"miner time equal to the parent is ignored", time.Unix(int64(parent.Time)+3, 0), parent.Time, 1},
	}
	for _, c := range cases {
		f2 := newP1Fixture(t)
		p2 := f2.chainOf(1)[1]
		now := c.now
		f2.hs.now = func() time.Time { return now }
		got := prepareOn(f2, p2, c.minerTime)
		if want := p2.Time + c.wantSlots*p1TestSlot; got.Time != want {
			t.Errorf("%s: stamp = parent + %d, want parent + %d", c.name, int64(got.Time)-int64(p2.Time), want-p2.Time)
		}
		if (got.Time-p1TestGenesisT0)%p1TestSlot != 0 {
			t.Errorf("%s: stamp %d off the grid", c.name, got.Time)
		}
	}
}

func TestProfile1ExtraDecodeIsStrict(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(2)
	qc := f.commitQC(2, hs[2].Hash(), []int{0, 1, 2}, true)
	extra, err := buildHeaderExtra(3, qc)
	if err != nil {
		t.Fatal(err)
	}
	view, got, seal, err := decodeProfile1Extra(extra)
	if err != nil || view != 3 || got == nil || got.View != 2 || len(seal) != extraSealLen {
		t.Fatalf("decode = view %d qc %v seal %d err %v", view, got, len(seal), err)
	}
	// The legacy raw-QC layout (no seal) is refused on profile 1.
	raw, _ := encodeQC(qc)
	legacy := append(append([]byte{}, extra[:extraMinLen]...), raw...)
	if _, _, _, err := decodeProfile1Extra(legacy); !errors.Is(err, ErrProfile1Extra) {
		t.Fatalf("legacy layout = %v, want %v", err, ErrProfile1Extra)
	}
}
