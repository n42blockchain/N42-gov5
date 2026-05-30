package ethel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/params"
)

// emptyTrieRoot is the root of an empty MPT — both the empty-state stateRoot and
// the empty-receipts receiptRoot. EthReceiptHash(nil) derives exactly this.
func emptyTrieRoot() types.Hash { return EthReceiptHash(nil) }

// mkHeader builds a post-merge, pre-Shanghai synthetic header (Difficulty 0,
// timestamp before the Shanghai fork → no block reward, no withdrawals and no
// Prague system-contract calls, so an empty block leaves state unchanged).
// Time is passed explicitly (not derived from num*12, which for a post-merge
// block number lands far in the future and wrongly activates later forks).
func mkHeader(num, time uint64, parent types.Hash, root, receipt types.Hash) *block.Header {
	return &block.Header{
		ParentHash:  parent,
		Coinbase:    types.HexToAddress("0x0000000000000000000000000000000000000001"),
		Root:        root,
		ReceiptHash: receipt,
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(num),
		GasLimit:    30000000,
		GasUsed:     0,
		Time:        time,
		BaseFee:     uint256.NewInt(1000000000),
	}
}

// post-merge, pre-Shanghai timestamps (Shanghai mainnet ~1681338455).
const (
	tsAnchor = uint64(1663300000)
	tsHead   = uint64(1663300012)
)

// setup builds a trusted HeaderChain (anchor N-1 -> head N) over an empty state
// and the matching MinimalVerifyInput for an empty post-merge block N.
func setup(t *testing.T) (*stateless.HeaderChain, *params.ChainConfig, *MinimalVerifyInput) {
	t.Helper()
	const N = uint64(16000000)
	empty := emptyTrieRoot()

	anchor := mkHeader(N-1, tsAnchor, types.Hash{}, empty, empty)
	headerN := mkHeader(N, tsHead, anchor.Hash(), empty, empty)

	hc, err := stateless.NewHeaderChain(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if err := hc.Extend(headerN); err != nil {
		t.Fatalf("extend: %v", err)
	}

	in := &MinimalVerifyInput{
		Header:  headerN,
		Body:    &GethBodyResult{},
		Witness: nil,
		Proof:   &stateless.BlockProof{Number: N, AccountProof: nil, Changes: nil},
	}
	return hc, params.EthereumMainnetChainConfig, in
}

// TestVerifyBlockFull_AllLayersPass proves the unified verifier accepts a
// self-consistent empty post-merge block across all three trust layers.
func TestVerifyBlockFull_AllLayersPass(t *testing.T) {
	hc, cfg, in := setup(t)
	engine := NewEthReplayEngine(cfg)
	if err := VerifyBlockFull(hc, cfg, engine, in); err != nil {
		t.Fatalf("VerifyBlockFull: %v", err)
	}
}

// TestVerifyBlockFull_RejectTamperedHeader: a header that does not hash to the
// trusted chain hash is rejected at layer ①.
func TestVerifyBlockFull_RejectTamperedHeader(t *testing.T) {
	hc, cfg, _ := setup(t) // hc trusts the canonical headerN
	engine := NewEthReplayEngine(cfg)
	const N = uint64(16000000)
	empty := emptyTrieRoot()
	// A DIFFERENT header at N (fresh object, different GasLimit → different,
	// uncached Hash) must not pass as the trusted block.
	anchor := mkHeader(N-1, tsAnchor, types.Hash{}, empty, empty)
	fake := mkHeader(N, tsHead, anchor.Hash(), empty, empty)
	fake.GasLimit = 12345
	in := &MinimalVerifyInput{Header: fake, Body: &GethBodyResult{}, Proof: &stateless.BlockProof{Number: N}}
	if err := VerifyBlockFull(hc, cfg, engine, in); err == nil {
		t.Fatal("expected layer① rejection of tampered header")
	}
}

// TestVerifyBlockFull_RejectBadReceiptRoot: a header whose ReceiptHash is wrong
// for the (empty) block is rejected at layer ② (gasUsed matched, receipts
// diverge). The header is anchored as-is so layer ① passes.
func TestVerifyBlockFull_RejectBadReceiptRoot(t *testing.T) {
	const N = uint64(16000000)
	empty := emptyTrieRoot()
	anchor := mkHeader(N-1, tsAnchor, types.Hash{}, empty, empty)
	bad := mkHeader(N, tsHead, anchor.Hash(), empty, types.BytesToHash([]byte{0xde, 0xad}))
	hc, _ := stateless.NewHeaderChain(anchor)
	if err := hc.Extend(bad); err != nil {
		t.Fatal(err)
	}
	cfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(cfg)
	in := &MinimalVerifyInput{Header: bad, Body: &GethBodyResult{}, Proof: &stateless.BlockProof{Number: N}}
	if err := VerifyBlockFull(hc, cfg, engine, in); err == nil {
		t.Fatal("expected layer② rejection of wrong receiptRoot")
	}
}

// TestVerifyBlockFull_RejectBadStateProof: a changeset that recomputes to a
// different root than the trusted header.stateRoot is rejected at layer ③.
func TestVerifyBlockFull_RejectBadStateProof(t *testing.T) {
	hc, cfg, in := setup(t)
	engine := NewEthReplayEngine(cfg)
	// Insert a phantom account: empty pre-state + this change no longer hashes
	// to the empty post-state root the header claims.
	var e stateless.AccountChange
	e.AddrHash = types.BytesToHash([]byte{0x01, 0x02, 0x03})
	e.Nonce = 1
	e.Balance.SetUint64(99)
	in.Proof.Changes = []stateless.AccountChange{e}
	if err := VerifyBlockFull(hc, cfg, engine, in); err == nil {
		t.Fatal("expected layer③ rejection of inconsistent changeset")
	}
}
