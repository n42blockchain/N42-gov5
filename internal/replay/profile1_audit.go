// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// P1Auditor checks replayed profile-1 blocks the way a follower does, with
// nothing but the genesis: the header codecs, the hotstuff header rules
// (profile-1 checks, embedded CommitQC, seal, committee-evidence link), the
// committee evidence against the pool public keys and against the live
// committee pool's own output, and the body commitments.

package replay

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// P1HeaderCodecRoundTrip encodes h in every header codec (RLP, the
// proto/trailer Marshal, the compact storage codec) and checks each decodes
// back to the same hash and the same presence of elements 21-24.
func P1HeaderCodecRoundTrip(h *block.Header) error {
	want := h.Hash()
	check := func(name string, got *block.Header) error {
		if got.Hash() != want {
			return fmt.Errorf("%s round trip: hash %s, want %s", name, got.Hash().Hex(), want.Hex())
		}
		if (got.RequestsHash == nil) != (h.RequestsHash == nil) || (got.BlockAccessListHash == nil) != (h.BlockAccessListHash == nil) ||
			(got.SlotNumber == nil) != (h.SlotNumber == nil) || (got.MobileRegistryRoot == nil) != (h.MobileRegistryRoot == nil) {
			return fmt.Errorf("%s round trip: elements 21-24 presence changed", name)
		}
		return nil
	}
	enc, err := rlp.EncodeToBytes(h)
	if err != nil {
		return fmt.Errorf("rlp encode: %w", err)
	}
	var r block.Header
	if err := rlp.DecodeBytes(enc, &r); err != nil {
		return fmt.Errorf("rlp decode: %w", err)
	}
	if err := check("rlp", &r); err != nil {
		return err
	}
	pb, err := h.Marshal()
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	var p block.Header
	if err := p.Unmarshal(pb); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if err := check("proto", &p); err != nil {
		return err
	}
	var c block.Header
	if err := c.Unmarshal(h.MarshalCompact()); err != nil {
		return fmt.Errorf("compact unmarshal: %w", err)
	}
	return check("compact", &c)
}

// p1Window is a ChainHeaderReader over the genesis and the most recent
// audited headers (the profile-1 checks read the genesis, the parent and the
// embedded QC's block, which is the parent).
type p1Window struct {
	cfg     *params.ChainConfig
	genesis *block.Header
	byHash  map[types.Hash]*block.Header
	order   []types.Hash
}

const p1WindowLen = 16

func (w *p1Window) add(h *block.Header) {
	w.byHash[h.Hash()] = h
	w.order = append(w.order, h.Hash())
	if len(w.order) > p1WindowLen {
		delete(w.byHash, w.order[0])
		w.order = w.order[1:]
	}
}

func (w *p1Window) Config() *params.ChainConfig { return w.cfg }
func (w *p1Window) CurrentBlock() block.IBlock  { return nil }
func (w *p1Window) GetHeader(h types.Hash, _ *uint256.Int) block.IHeader {
	if hd := w.byHash[h]; hd != nil {
		return hd
	}
	return nil
}
func (w *p1Window) GetHeaderByNumber(n *uint256.Int) block.IHeader {
	if n != nil && n.IsZero() {
		return w.genesis
	}
	for _, h := range w.byHash {
		if h.Number.Cmp(n) == 0 {
			return h
		}
	}
	return nil
}
func (w *p1Window) GetHeaderByHash(h types.Hash) (block.IHeader, error) {
	if hd := w.byHash[h]; hd != nil {
		return hd, nil
	}
	if h == w.genesis.Hash() {
		return w.genesis, nil
	}
	return nil, errors.New("not found")
}
func (w *p1Window) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (w *p1Window) GetBlockByNumber(*uint256.Int) (block.IBlock, error) {
	return nil, errors.New("not available")
}

// P1Auditor verifies a replayed profile-1 chain block by block, in order.
type P1Auditor struct {
	cfg       *params.ChainConfig
	hs        *hotstuff.HotStuff
	window    *p1Window
	pool      *blspool.Pool         // the live committee pool (byte identity)
	pubs      []blscommon.PublicKey // pool public keys only (signature check)
	poolCfg   *params.HotStuffCommitteePoolConfig
	committee *blspool.CommitteeScratch
}

// NewP1Auditor builds the auditor from the profile-1 chain config and the
// target genesis header. The committee pool is derived from
// committeePool.seedHex twice: public keys only for the signature check,
// and as the live node's pool for the byte-identity check.
func NewP1Auditor(cfg *params.ChainConfig, genesis *block.Header) (*P1Auditor, error) {
	if !cfg.IsProfile1() || cfg.HotStuff == nil || cfg.HotStuff.CommitteePool == nil {
		return nil, errors.New("audit: not a profile-1 config with a committee pool")
	}
	pc := cfg.HotStuff.CommitteePool
	seedB, err := hexutil.Decode(pc.SeedHex)
	if err != nil || len(seedB) != 32 {
		return nil, fmt.Errorf("audit: committeePool.seedHex: %v", err)
	}
	var seed [32]byte
	copy(seed[:], seedB)
	_, pubs, err := blspool.DeriveKeys(seed, pc.PoolSize, false)
	if err != nil {
		return nil, err
	}
	pool, err := blspool.NewSimulatedPool(blspool.PoolConfig{Seed: seed, PoolSize: pc.PoolSize, CommitteeSize: pc.CommitteeSize, RampBlocks: pc.RampBlocks})
	if err != nil {
		return nil, err
	}
	hs := hotstuff.New(cfg.HotStuff, cfg)
	hs.SetCommitteeEvidence(pool, nil)
	return &P1Auditor{
		cfg: cfg, hs: hs, pool: pool, pubs: pubs, poolCfg: pc,
		window:    &p1Window{cfg: cfg, genesis: genesis, byHash: map[types.Hash]*block.Header{}},
		committee: blspool.NewCommitteeScratch(),
	}, nil
}

// CheckBlock audits blk, whose stored committee evidence is ce. Blocks must
// be fed in order from block 1.
func (a *P1Auditor) CheckBlock(blk *block.Block, ce *rawdb.ConsensusEvidence) error {
	h := blk.Header().(*block.Header)
	n := h.Number.Uint64()
	if err := P1HeaderCodecRoundTrip(h); err != nil {
		return fmt.Errorf("block %d codecs: %w", n, err)
	}
	if err := a.hs.VerifyHeader(a.window, h, true); err != nil {
		return fmt.Errorf("block %d header: %w", n, err)
	}
	// The Round-1 vote gate as at the block's own slot (B4's clock bound).
	if err := a.hs.StaticHeaderCheck(a.window, h, time.Unix(int64(h.Time), 0)); err != nil {
		return fmt.Errorf("block %d vote gate: %w", n, err)
	}
	if got := block.TxRootAt(blk.Transactions(), h.Time); got != h.TxHash {
		return fmt.Errorf("block %d: transactions root %s, header %s", n, got.Hex(), h.TxHash.Hex())
	}
	var rewards []*block.Reward
	if bd, ok := blk.Body().(*block.Body); ok && bd != nil {
		rewards = bd.Rewards
	}
	if h.WithdrawalsHash == nil || hash.DeriveSha(block.Rewards(rewards)) != *h.WithdrawalsHash {
		return fmt.Errorf("block %d: withdrawalsRoot does not commit to the body rewards (B6)", n)
	}
	if err := a.checkCE(h, ce); err != nil {
		return fmt.Errorf("block %d committee evidence: %w", n, err)
	}
	a.window.add(h)
	return nil
}

// checkCE verifies the committee evidence of h: it names h, it is exactly
// what the live committee pool builds for h, and its aggregate signature
// verifies against the committee's public keys from the pool.
func (a *P1Auditor) checkCE(h *block.Header, ce *rawdb.ConsensusEvidence) error {
	if ce == nil {
		return errors.New("missing")
	}
	n, bh := h.Number.Uint64(), h.Hash()
	if ce.View != n || ce.BlockHash != bh {
		return fmt.Errorf("names view %d block %x, want %d %x", ce.View, ce.BlockHash[:8], n, bh[:8])
	}
	live, err := a.pool.BuildSimulatedCE(n, bh, h.ReceiptHash)
	if err != nil {
		return err
	}
	if !bytes.Equal(live.Marshal(), ce.Marshal()) {
		return errors.New("differs from the live committee pool's evidence")
	}
	active := blspool.ActivePool(n, a.poolCfg.PoolSize, a.poolCfg.CommitteeSize, a.poolCfg.RampBlocks)
	members := a.committee.Committee(n, bh, active, a.poolCfg.CommitteeSize)
	if int(ce.SignerCount) != len(members) || len(members) != a.poolCfg.CommitteeSize {
		return fmt.Errorf("%d signers, committee %d of %d", ce.SignerCount, len(members), a.poolCfg.CommitteeSize)
	}
	pubs := make([]blscommon.PublicKey, 0, len(members))
	for i, m := range members {
		if i/8 >= len(ce.SignersPacked) || ce.SignersPacked[i/8]&(1<<uint(i%8)) == 0 {
			return fmt.Errorf("member %d (pool slot %d) did not sign", i, m)
		}
		pubs = append(pubs, a.pubs[m])
	}
	sig, err := bls.SignatureFromBytes(ce.AggregateSignature[:])
	if err != nil {
		return err
	}
	if !sig.Verify(bls.AggregateMultiplePubkeys(pubs), blspool.SigningMessage(n, bh)) {
		return errors.New("aggregate signature does not verify against the pool public keys")
	}
	return nil
}

// ActiveAt is the committee pool's active size at block n (ramp).
func (a *P1Auditor) ActiveAt(n uint64) int {
	return blspool.ActivePool(n, a.poolCfg.PoolSize, a.poolCfg.CommitteeSize, a.poolCfg.RampBlocks)
}
