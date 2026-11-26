// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

const (
	p1tGasLimit = 30_000_000
	p1tGenesisT = 1678174066 // chain 94's genesis time
)

var p1tSeed = [32]byte{0x03, 0xc7, 0x5d}

// p1tChain is a profile-1 config with four deterministic test validators,
// tenure 2, an 8 s grid and a small committee pool.
func p1tChain(t *testing.T) (*params.ChainConfig, []blscommon.SecretKey, *block.Header) {
	t.Helper()
	// Deferred execution from block 0, as the replay engine sets it: block
	// assembly must not overwrite the carried receipts root and bloom.
	prev := block.DeferredExecutionTime
	block.DeferredExecutionTime = 1
	t.Cleanup(func() { block.DeferredExecutionTime = prev })
	var sks []blscommon.SecretKey
	var vcs []params.HotStuffValidatorConfig
	for i := 0; i < 4; i++ {
		seed := sha256.Sum256([]byte(fmt.Sprintf("n42-replay-p1-test-validator-%d", i)))
		seed[0] = 0
		sk, err := bls.SecretKeyFromBytes(seed[:])
		if err != nil {
			t.Fatal(err)
		}
		pk := sk.PublicKey().Marshal()
		addr := types.BytesToAddress(crypto.Keccak256(pk)[12:])
		sks = append(sks, sk)
		vcs = append(vcs, params.HotStuffValidatorConfig{Address: addr.Hex(), BLSKey: hexutil.Encode(pk)})
	}
	cfg := &params.ChainConfig{
		ChainID: big.NewInt(94), N42Profile: params.N42Profile1,
		LondonBlock: big.NewInt(0), ShanghaiTime: big.NewInt(0), CancunTime: big.NewInt(0),
		PragueTime: big.NewInt(0), AmsterdamTime: big.NewInt(0),
		HotStuff: &params.HotStuffConfig{
			Validators: vcs, LeaderTenure: 2, BaseTimeoutMs: 6000, MaxTimeoutMs: 30000,
			BlockIntervalMs: 8000, EpochLength: 200, DeferredExecutionDepth: 1,
			GasLimit: p1tGasLimit, MaxBlockBytes: 64 << 20,
			CommitteePool: &params.HotStuffCommitteePoolConfig{
				SeedHex: hexutil.Encode(p1tSeed[:]), PoolSize: 64, CommitteeSize: 8, RampBlocks: 4,
			},
		},
	}
	zero := uint64(0)
	rq, requests := hash.EmptyRootHash, rawdb.EmptyRequestsHash
	genesis := &block.Header{
		Difficulty: uint256.NewInt(0), Number: uint256.NewInt(0), GasLimit: p1tGasLimit,
		Time: p1tGenesisT, BaseFee: uint256.NewInt(params.InitialBaseFee),
		TxHash: hash.EmptyRootHash, ReceiptHash: hash.EmptyRootHash, Root: types.Hash{0x01},
		WithdrawalsHash: &rq, BlobGasUsed: &zero, ExcessBlobGas: &zero,
		ParentBeaconRoot: new(types.Hash), RequestsHash: &requests, SlotNumber: &zero,
		MobileRegistryRoot: new(types.Hash),
	}
	return cfg, sks, genesis
}

// TestP1SealedChainPassesFollowerChecks seals a short chain with dense
// source times (3 s apart: slots move ahead of the source clock), a source
// gap and executed results that change from block to block, then audits it
// as a follower: every hotstuff profile-1 header rule, the embedded CommitQC
// and seal, the committee-evidence link and evidence, and all header codecs.
func TestP1SealedChainPassesFollowerChecks(t *testing.T) {
	cfg, sks, genesis := p1tChain(t)
	sealer, err := NewP1Sealer(cfg, genesis, sks)
	if err != nil {
		t.Fatal(err)
	}
	pc := cfg.HotStuff.CommitteePool
	rs, err := NewBLSResealer(BLSResealConfig{Seed: p1tSeed, PoolSize: pc.PoolSize, CommitteeSize: pc.CommitteeSize, RampBlocks: pc.RampBlocks})
	if err != nil {
		t.Fatal(err)
	}
	audit, err := NewP1Auditor(cfg, genesis)
	if err != nil {
		t.Fatal(err)
	}

	srcTimes := []uint64{p1tGenesisT + 3, p1tGenesisT + 6, p1tGenesisT + 9, p1tGenesisT + 400, p1tGenesisT + 401, p1tGenesisT + 409, p1tGenesisT + 9000}
	parent := genesis
	var times []uint64
	var parentCE *rawdb.ConsensusEvidence
	registry := types.Hash{}
	for i, st := range srcTimes {
		n := uint64(i + 1)
		if n == 5 {
			registry = types.Hash{0x42} // the source anchors a registry root
		}
		h, err := sealer.Context(parent, n, st, registry)
		if err != nil {
			t.Fatal(err)
		}
		if h.Coinbase != sealer.Leader(n) {
			t.Fatalf("block %d coinbase", n)
		}
		h.TxHash = block.TxRootAt(nil, h.Time)
		wh := hash.DeriveSha(block.Rewards(nil))
		h.WithdrawalsHash = &wh
		var pbr types.Hash
		if parentCE != nil && n > 1 {
			pbr = parentCE.BeaconRoot()
		}
		if err := sealer.Seal(h, pbr); err != nil {
			t.Fatal(err)
		}
		blk := block.NewBlockFromReceipt(h, nil, nil, nil, nil).(*block.Block)
		ce := rs.BuildCE(n, blk.Hash(), h.ReceiptHash)
		if err := audit.CheckBlock(blk, ce); err != nil {
			t.Fatalf("block %d (time %d, source %d): %v", n, h.Time, st, err)
		}
		// Block n's own executed result, carried by block n+1.
		exec := rawdb.ExecutedResult{Root: types.Hash{byte(n), 0xaa}, ReceiptHash: types.Hash{byte(n), 0xbb}, GasUsed: 21000 * n, RequestsHash: rawdb.EmptyRequestsHash}
		qc := sealer.Commit(h, exec)
		if qc.BlockHash != blk.Hash() || qc.View != n {
			t.Fatalf("block %d: CommitQC names %d %x", n, qc.View, qc.BlockHash[:4])
		}
		parent, parentCE = h, ce
		times = append(times, h.Time-p1tGenesisT)
	}

	// Slot offsets: the dense 3 s source blocks take consecutive slots (8,
	// 16, 24); a later source time takes the first slot not before it.
	want := []uint64{8, 16, 24, 400, 408, 416, 9000}
	for i := range want {
		if times[i] != want[i] {
			t.Fatalf("slot offsets %v, want %v", times, want)
		}
	}
	// The continuation state names the head.
	st := ContinuationState(sealer.parentQC)
	if st.View != parent.Number.Uint64()+1 || st.LastCommittedQC.BlockHash != parent.Hash() {
		t.Fatalf("continuation state %+v", st)
	}
}

// A header that breaks a profile-1 rule is refused by the auditor: the
// tests above would not catch a vacuous auditor otherwise.
func TestP1AuditorRefusesTamperedHeaders(t *testing.T) {
	cfg, sks, genesis := p1tChain(t)
	pc := cfg.HotStuff.CommitteePool
	rs, err := NewBLSResealer(BLSResealConfig{Seed: p1tSeed, PoolSize: pc.PoolSize, CommitteeSize: pc.CommitteeSize, RampBlocks: pc.RampBlocks})
	if err != nil {
		t.Fatal(err)
	}
	build := func(tamper func(h *block.Header)) error {
		sealer, err := NewP1Sealer(cfg, genesis, sks)
		if err != nil {
			t.Fatal(err)
		}
		audit, err := NewP1Auditor(cfg, genesis)
		if err != nil {
			t.Fatal(err)
		}
		h, err := sealer.Context(genesis, 1, p1tGenesisT+8, types.Hash{})
		if err != nil {
			t.Fatal(err)
		}
		h.TxHash = block.TxRootAt(nil, h.Time)
		wh := hash.DeriveSha(block.Rewards(nil))
		h.WithdrawalsHash = &wh
		tamper(h)
		if err := sealer.Seal(h, types.Hash{}); err != nil {
			t.Fatal(err)
		}
		blk := block.NewBlockFromReceipt(h, nil, nil, nil, nil).(*block.Block)
		return audit.CheckBlock(blk, rs.BuildCE(1, blk.Hash(), h.ReceiptHash))
	}
	if err := build(func(*block.Header) {}); err != nil {
		t.Fatalf("untampered: %v", err)
	}
	for name, tamper := range map[string]func(h *block.Header){
		"time off grid":   func(h *block.Header) { h.Time++ },
		"gas limit":       func(h *block.Header) { h.GasLimit-- },
		"base fee":        func(h *block.Header) { h.BaseFee = uint256.NewInt(7) },
		"mix hash":        func(h *block.Header) { h.MixDigest = types.Hash{1} },
		"coinbase":        func(h *block.Header) { h.Coinbase = types.Address{1} },
		"element 22":      func(h *block.Header) { x := types.Hash{}; h.BlockAccessListHash = &x },
		"blob gas":        func(h *block.Header) { one := uint64(1); h.BlobGasUsed = &one },
		"requests absent": func(h *block.Header) { h.RequestsHash = nil },
	} {
		if err := build(tamper); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The replay resealer and the live committee pool produce byte-identical
// evidence for the same (number, hash, receiptsRoot), including across the
// ramp, so a live node continues a resealed chain (B5 link).
func TestBLSResealerMatchesLiveCommitteePool(t *testing.T) {
	rs, err := NewBLSResealer(BLSResealConfig{Seed: p1tSeed, PoolSize: 64, CommitteeSize: 8, RampBlocks: 4})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := blspool.NewSimulatedPool(blspool.PoolConfig{Seed: p1tSeed, PoolSize: 64, CommitteeSize: 8, RampBlocks: 4})
	if err != nil {
		t.Fatal(err)
	}
	for n := uint64(1); n <= 8; n++ {
		bh := types.Hash{byte(n), 0x11}
		rr := types.Hash{byte(n), 0x22}
		a := rs.BuildCE(n, bh, rr)
		b, err := pool.BuildSimulatedCE(n, bh, rr)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.Marshal(), b.Marshal()) {
			t.Fatalf("block %d: resealer and live pool evidence differ", n)
		}
		if covered, ok, err := pool.VerifyCE(a); err != nil || !ok || covered != 8 {
			t.Fatalf("block %d: live pool refuses resealer evidence: %d %v %v", n, covered, ok, err)
		}
	}
}

// The committee-pool flags must agree with the target config's pool.
func TestApplyCommitteePoolConfig(t *testing.T) {
	cfg, _, _ := p1tChain(t)
	c := ConfigV2{ChainConfig: cfg, BLSReseal: true}
	if err := applyCommitteePoolConfig(&c); err != nil || c.BLSSeed != p1tSeed || c.BLSPoolSize != 64 || c.BLSCommitteeSize != 8 || c.BLSRampBlocks != 4 {
		t.Fatalf("defaults from config: %v %+v", err, c)
	}
	c = ConfigV2{ChainConfig: cfg, BLSReseal: true, BLSPoolSize: 200000}
	if err := applyCommitteePoolConfig(&c); err == nil {
		t.Fatal("a pool size that differs from the config was accepted")
	}
	c = ConfigV2{ChainConfig: cfg, BLSReseal: true, BLSSeed: [32]byte{9}}
	if err := applyCommitteePoolConfig(&c); err == nil {
		t.Fatal("a seed that differs from the config was accepted")
	}
}

// A key that does not match its validator is refused.
func TestP1SealerRefusesWrongKeys(t *testing.T) {
	cfg, sks, genesis := p1tChain(t)
	sks[1], sks[2] = sks[2], sks[1]
	if _, err := NewP1Sealer(cfg, genesis, sks); err == nil {
		t.Fatal("swapped keys accepted")
	}
	if _, err := NewP1Sealer(cfg, genesis, sks[:3]); err == nil {
		t.Fatal("missing key accepted")
	}
}

// The commit QC verifies in the H2-v4 commit domain with every validator.
func TestP1CommitQCVerifies(t *testing.T) {
	cfg, sks, genesis := p1tChain(t)
	s, err := NewP1Sealer(cfg, genesis, sks)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := s.Context(genesis, 1, p1tGenesisT+8, types.Hash{})
	h.TxHash = block.TxRootAt(nil, h.Time)
	wh := hash.DeriveSha(block.Rewards(nil))
	h.WithdrawalsHash = &wh
	if err := s.Seal(h, types.Hash{}); err != nil {
		t.Fatal(err)
	}
	qc := s.Commit(h, rawdb.ExecutedResult{})
	var pubs []blscommon.PublicKey
	for _, sk := range sks {
		pubs = append(pubs, sk.PublicKey())
	}
	sig, err := bls.SignatureFromBytes(qc.AggregateSignature)
	if err != nil {
		t.Fatal(err)
	}
	msg := hotstuff.H2V4CommitSigningMessage(s.Identity(), qc.View, qc.BlockHash, types.Hash{})
	if !sig.Verify(bls.AggregateMultiplePubkeys(pubs), msg) {
		t.Fatal("CommitQC does not verify")
	}
}
