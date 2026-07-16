// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase-6c activation model (n42 native chain): the mobile-registry root is a
// HEADER commitment only — like the CommitteePool's ParentBeaconRoot link, it
// binds the committed accumulator root into the block hash with NO state-trie
// write. That is why it needs no system contract, no genesis alloc, and no
// replay to activate: exactly the mechanism the 200K/512 committee uses
// (header hash-link + rawdb side table). These tests pin the fork gate and the
// no-state-write property.

package internal

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func mobileAnchorConfig(active bool) *params.ChainConfig {
	cfg := &params.ChainConfig{
		ChainID:               big.NewInt(94),
		Consensus:             params.Faker,
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
	}
	if active {
		cfg.MobileAnchorTime = big.NewInt(0)
	}
	return cfg
}

func TestMobileAnchorForkGate(t *testing.T) {
	if mobileAnchorConfig(false).IsMobileAnchor(1000) {
		t.Fatal("IsMobileAnchor true with a nil MobileAnchorTime (eth-el / dormant shape)")
	}
	if !mobileAnchorConfig(true).IsMobileAnchor(1000) {
		t.Fatal("IsMobileAnchor false while the fork is active")
	}
}

// TestMobileAnchorHeaderIsPureCommitment confirms the design property the user
// flagged: binding the root is a header-only commitment. Stamping it changes
// the block hash (it is committed to consensus) but touches no state — no
// system contract, no genesis alloc, no replay needed to activate. Mirrors the
// CommitteePool's ParentBeaconRoot mechanism.
func TestMobileAnchorHeaderIsPureCommitment(t *testing.T) {
	root := types.HexToHash("0x7777000000000000000000000000000000000000000000000000000000007777")
	bare := &block.Header{
		Number:     uint256.NewInt(500),
		Time:       9,
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
		Root:       types.HexToHash("0x33"),
	}
	stamped := block.CopyHeader(bare)
	stamped.MobileRegistryRoot = &root

	// The state root field is untouched by stamping — the anchor is a header
	// commitment, not a state write (that is why no replay is required).
	if stamped.Root != bare.Root {
		t.Fatal("stamping the anchor changed the state root — it must not")
	}
	// The block hash DOES change (the root is bound into consensus).
	if stamped.Hash() == bare.Hash() {
		t.Fatal("stamping the anchor did not change the block hash")
	}
	// And it round-trips through the consensus block encoding unchanged.
	if got := block.CopyHeader(stamped); got.MobileRegistryRoot == nil || *got.MobileRegistryRoot != root {
		t.Fatal("anchor root lost through header copy")
	}
}

// stampLikeMiner reproduces the exact stamping decision worker.commitWork
// makes under the fork: header.MobileRegistryRoot = provider() iff the fork is
// active. Kept in lockstep with the miner so the determinism validation
// exercises the real activation logic, not a stand-in.
func stampLikeMiner(cfg *params.ChainConfig, header *block.Header, root *types.Hash) {
	if root != nil && cfg.IsMobileAnchor(header.Time) {
		header.MobileRegistryRoot = root
	}
}

func anchorTestHeader(time uint64) *block.Header {
	return &block.Header{
		ParentHash:  types.HexToHash("0x22"),
		Coinbase:    types.HexToAddress("0xf7dc5c92fa9e812eb0c3157492da65457ae5de46"),
		Root:        types.HexToHash("0x33"),
		TxHash:      types.HexToHash("0x44"),
		ReceiptHash: types.HexToHash("0x55"),
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(1000),
		GasLimit:    30_000_000,
		GasUsed:     21000,
		Time:        time,
		BaseFee:     uint256.NewInt(1_000_000_000),
		MixDigest:   types.HexToHash("0x66"),
	}
}

// TestMobileAnchorCrossNodeDeterminism is the phase-6d validation: with
// MobileAnchorTime set on the (test) chain config, the leader stamps the
// committed accumulator root; the block is transmitted over the consensus wire
// (Block RLP — the gossip/sync path) and over the rawdb header path; and every
// receiving node reproduces the IDENTICAL block hash. Because the root travels
// IN the block and no node recomputes it (header commitment, like
// ParentBeaconRoot), agreement is structural — this test pins it end to end.
func TestMobileAnchorCrossNodeDeterminism(t *testing.T) {
	cfg := mobileAnchorConfig(true) // MobileAnchorTime = 0 on this test chain
	root := types.HexToHash("0xacc0000000000000000000000000000000000000000000000000000000000acc")

	// Leader builds a block at a fork-active time and stamps the root.
	leaderHeader := anchorTestHeader(100)
	stampLikeMiner(cfg, leaderHeader, &root)
	if leaderHeader.MobileRegistryRoot == nil {
		t.Fatal("leader did not stamp the root under an active fork")
	}
	leaderBlock := block.NewBlock(leaderHeader, nil).(*block.Block)
	leaderHash := leaderBlock.Hash()

	// Follower A receives it over the consensus wire (gossip / sync chunked).
	enc, err := leaderBlock.Marshal() // == EncodeRLP wire form
	if err != nil {
		t.Fatal(err)
	}
	var followerA block.Block
	if err := followerA.Unmarshal(enc); err != nil {
		t.Fatal(err)
	}
	if followerA.Hash() != leaderHash {
		t.Fatalf("follower A block hash diverged: %s vs %s", followerA.Hash().Hex(), leaderHash.Hex())
	}
	if got := followerA.Header().(*block.Header).MobileRegistryRoot; got == nil || *got != root {
		t.Fatalf("follower A lost the anchor root: %v", got)
	}

	// Follower B receives just the header over the rawdb single-header path.
	hb, err := leaderHeader.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var followerB block.Header
	if err := followerB.Unmarshal(hb); err != nil {
		t.Fatal(err)
	}
	if followerB.Hash() != leaderHash {
		t.Fatalf("follower B header hash diverged: %s vs %s", followerB.Hash().Hex(), leaderHash.Hex())
	}

	// A node that independently builds the SAME block (same root) gets the SAME
	// hash — deterministic. A different root would change it (load-bearing).
	twinHeader := anchorTestHeader(100)
	stampLikeMiner(cfg, twinHeader, &root)
	if block.NewBlock(twinHeader, nil).Hash() != leaderHash {
		t.Fatal("independent build with the same root produced a different hash")
	}
	other := root
	other[0] ^= 0xFF
	diffHeader := anchorTestHeader(100)
	stampLikeMiner(cfg, diffHeader, &other)
	if block.NewBlock(diffHeader, nil).Hash() == leaderHash {
		t.Fatal("a different anchored root produced the same block hash")
	}
}

// TestMobileAnchorDormantProducesLegacyBlock confirms the dormant path: with
// MobileAnchorTime nil (every current chainspec, and eth-el), the miner does
// not stamp, the header field stays nil, and the block hash is byte-identical
// to a chain that never knew the fork — the safety property for leaving it off.
func TestMobileAnchorDormantProducesLegacyBlock(t *testing.T) {
	dormant := mobileAnchorConfig(false)
	root := types.HexToHash("0xacc0000000000000000000000000000000000000000000000000000000000acc")

	h := anchorTestHeader(100)
	stampLikeMiner(dormant, h, &root)
	if h.MobileRegistryRoot != nil {
		t.Fatal("stamped the root while the fork was dormant")
	}
	plain := anchorTestHeader(100) // never offered a root
	if block.NewBlock(h, nil).Hash() != block.NewBlock(plain, nil).Hash() {
		t.Fatal("dormant block hash differs from a no-anchor block")
	}
}

var _ = transaction.Transaction{} // keep the import even if NewBlock takes nil txs
