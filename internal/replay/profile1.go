// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Profile-1 re-derivation of replayed blocks (mixed production standard,
// profile 1; owner decisions of 2026-11-26 for replay v2): every replayed
// block gets the header shape and the consensus evidence a live profile-1
// block has.
//
//   - view = block number (a replayed chain has no timeouts); coinbase is the
//     leader of the view, (view / leaderTenure) mod n (A15, B3);
//   - timestamp: the slot a leader would have stamped had it proposed at the
//     source block's time, on the genesis grid (AM27): G + s*I with
//     s = max(sParent+1, ceil((srcTime-G)/I));
//   - gasLimit = hotstuff.gasLimit (A23); baseFee = EIP-1559 of the parent
//     header as written (B14);
//   - Root, ReceiptHash, Bloom, GasUsed and RequestsHash are the parent's
//     executed values (A10, AM25, depth 1); block 1 carries the genesis ones;
//   - difficulty, nonce, mixHash and ommersHash zero (B3); blob gas fields 0
//     (B9); element 22 absent (B12); slotNumber = number (B10);
//     mobileRegistryRoot always present (B11);
//   - extraData = "N42H" || view || commit QC of the parent || seal (B1, B2):
//     the parent's CommitQC signed by every validator of the genesis set in
//     the H2-v4 commit domain, and the leader's BLS seal over sealHash;
//   - parentBeaconRoot = Blake3 of the parent's committee evidence (B5), the
//     512-member committee of the 200,000-key mobile pool (BLSResealer).
//
// Execution uses this header as its block context, so a follower that
// re-executes the block sees the same NUMBER, TIMESTAMP, COINBASE, BASEFEE,
// GASLIMIT and PREVRANDAO (zero).

package replay

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// p1SealLen is the BLS seal at the end of a profile-1 extraData (B1).
const p1SealLen = 96

// P1Sealer derives profile-1 headers and their validator consensus evidence
// for a replayed chain. Not safe for concurrent use: blocks are sealed in
// order.
type P1Sealer struct {
	cfg      *params.ChainConfig
	identity hotstuff.H2V4ChainIdentity
	sks      []blscommon.SecretKey
	addrs    []types.Address
	keySum   [32]byte // sum of every validator secret scalar mod r
	signers  []bool   // every validator signs each CommitQC
	tenure   uint64
	genesisT uint64
	interval uint64 // seconds

	prevExec rawdb.ExecutedResult        // executed result of the parent
	parentQC *hotstuff.QuorumCertificate // CommitQC on the parent (nil before block 2)
}

// NewP1Sealer builds the sealer for a profile-1 target chain whose genesis
// header is genesis. sks are the BLS secret keys of config.hotstuff.validators
// in list order; each must match its blsKey.
func NewP1Sealer(cfg *params.ChainConfig, genesis *block.Header, sks []blscommon.SecretKey) (*P1Sealer, error) {
	if !cfg.IsProfile1() || cfg.HotStuff == nil {
		return nil, errors.New("replay profile 1: target chain config is not n42Profile 1")
	}
	hs := cfg.HotStuff
	if len(sks) != len(hs.Validators) || len(sks) == 0 {
		return nil, fmt.Errorf("replay profile 1: %d validator keys for %d validators", len(sks), len(hs.Validators))
	}
	ivMs := cfg.Profile1SlotMs()
	if ivMs == 0 || ivMs%1000 != 0 {
		// A sub-second grid (AM27a) stamps floor(slot start / 1000); not
		// needed for chain 94 (8000 ms) and not implemented here.
		return nil, fmt.Errorf("replay profile 1: blockIntervalMs %d is not a whole number of seconds", ivMs)
	}
	if hs.GasLimit == 0 {
		return nil, errors.New("replay profile 1: hotstuff.gasLimit is zero")
	}
	if cfg.ChainID == nil || !cfg.ChainID.IsUint64() {
		return nil, errors.New("replay profile 1: chain id is not a uint64")
	}
	s := &P1Sealer{
		cfg:      cfg,
		identity: hotstuff.H2V4ChainIdentity{ChainID: cfg.ChainID.Uint64(), GenesisHash: genesis.Hash()},
		sks:      sks,
		signers:  make([]bool, len(sks)),
		tenure:   max(hs.LeaderTenure, 1),
		genesisT: genesis.Time,
		interval: ivMs / 1000,
	}
	var sum uint256.Int
	for i, sk := range sks {
		want, err := hexutil.Decode(hs.Validators[i].BLSKey)
		if err != nil {
			return nil, fmt.Errorf("replay profile 1: validator %d blsKey: %w", i, err)
		}
		if !bytes.Equal(sk.PublicKey().Marshal(), want) {
			return nil, fmt.Errorf("replay profile 1: key %d does not match validator %d blsKey %s", i, i, hs.Validators[i].BLSKey)
		}
		s.addrs = append(s.addrs, types.HexToAddress(hs.Validators[i].Address))
		s.signers[i] = true
		var k uint256.Int
		k.SetBytes(sk.Marshal())
		sum.AddMod(&sum, &k, &blsOrderU256)
	}
	s.keySum = sum.Bytes32()
	rh := rawdb.EmptyRequestsHash
	if genesis.RequestsHash != nil {
		rh = *genesis.RequestsHash
	}
	s.prevExec = rawdb.ExecutedResult{Root: genesis.Root, ReceiptHash: genesis.ReceiptHash, Bloom: genesis.Bloom, GasUsed: genesis.GasUsed, RequestsHash: rh}
	return s, nil
}

// Identity is the chain-bound H2-v4 identity the CommitQCs are signed under.
func (s *P1Sealer) Identity() hotstuff.H2V4ChainIdentity { return s.identity }

// Leader returns the coinbase of view (A15, B3).
func (s *P1Sealer) Leader(view uint64) types.Address {
	return s.addrs[(view/s.tenure)%uint64(len(s.addrs))]
}

// Resume restores the carried state when a replay continues an existing
// profile-1 target: the parent's executed result and its CommitQC.
func (s *P1Sealer) Resume(parentExec rawdb.ExecutedResult, parentQC *hotstuff.QuorumCertificate) {
	s.prevExec, s.parentQC = parentExec, parentQC
}

// SlotTime is the timestamp a leader stamps when it proposes on parentTime at
// wall-clock srcTime (AM27): the first grid slot after the parent's that is
// not in the past.
func (s *P1Sealer) SlotTime(parentTime, srcTime uint64) uint64 {
	return params.Profile1SlotTime(s.genesisT, s.interval, parentTime, time.Unix(int64(srcTime), 0))
}

// Context builds header number on parent with every field execution needs
// (number, time, coinbase, gasLimit, baseFee, prevRandao) and the carried
// deferred fields. The caller sets TxHash and WithdrawalsHash, then calls
// Seal.
func (s *P1Sealer) Context(parent *block.Header, number, srcTime uint64, registryRoot types.Hash) (*block.Header, error) {
	if parent.Number == nil || parent.Number.Uint64()+1 != number {
		return nil, fmt.Errorf("replay profile 1: parent of %d has number %v", number, parent.Number)
	}
	baseFee, overflow := uint256.FromBig(misc.CalcBaseFee(s.cfg, parent))
	if overflow {
		return nil, fmt.Errorf("replay profile 1: base fee overflow at %d", number)
	}
	zero := uint64(0)
	slot := number
	rh := s.prevExec.RequestsHash
	root := registryRoot
	h := &block.Header{
		ParentHash:         parent.Hash(),
		Coinbase:           s.Leader(number),
		Root:               s.prevExec.Root,
		ReceiptHash:        s.prevExec.ReceiptHash,
		Bloom:              s.prevExec.Bloom,
		Difficulty:         uint256.NewInt(0),
		Number:             uint256.NewInt(number),
		GasLimit:           s.cfg.HotStuff.GasLimit,
		GasUsed:            s.prevExec.GasUsed,
		Time:               s.SlotTime(parent.Time, srcTime),
		BaseFee:            baseFee,
		BlobGasUsed:        &zero,
		ExcessBlobGas:      ptrU64(0),
		RequestsHash:       &rh,
		SlotNumber:         &slot,
		MobileRegistryRoot: &root,
	}
	return h, nil
}

// Seal sets parentBeaconRoot, writes extraData with the parent's CommitQC and
// signs the seal as the leader of the header's view. h must come from
// Context with its TxHash and WithdrawalsHash set.
func (s *P1Sealer) Seal(h *block.Header, parentBeaconRoot types.Hash) error {
	pbr := parentBeaconRoot
	h.ParentBeaconRoot = &pbr
	view := h.Number.Uint64()
	extra, err := hotstuff.BuildHeaderExtra(view, s.parentQC)
	if err != nil {
		return err
	}
	h.Extra = extra
	h.ResetHashCache()
	digest := p1SealHash(h)
	leader := (view / s.tenure) % uint64(len(s.sks))
	sig := s.sks[leader].Sign(digest[:]).Marshal()
	copy(h.Extra[len(h.Extra)-p1SealLen:], sig)
	h.ResetHashCache()
	return nil
}

// Commit records block h as committed with exec, its own executed result:
// it returns h's CommitQC (every validator, H2-v4 commit domain, zero
// changesHash), which the next header embeds.
func (s *P1Sealer) Commit(h *block.Header, exec rawdb.ExecutedResult) *hotstuff.QuorumCertificate {
	view := h.Number.Uint64()
	hash := h.Hash()
	msg := hotstuff.H2V4CommitSigningMessage(s.identity, view, hash, types.Hash{})
	// Every validator signs the same message: the aggregate is
	// (sum sk_i) * H(m), one G2 multiplication (as BLSResealer.BuildCE).
	agg := bls.PrecomputeHash(msg).SignWithScalarSum(s.keySum)
	qc := &hotstuff.QuorumCertificate{
		View:               view,
		BlockHash:          hash,
		AggregateSignature: agg.Marshal(),
		Signers:            append([]bool(nil), s.signers...),
	}
	s.parentQC, s.prevExec = qc, exec
	return qc
}

// ContinuationState is the consensus state a live validator of the target
// chain resumes from after the replayed head: locked on and committed at the
// head's CommitQC, next view head+1 (the head's view is its number).
func ContinuationState(headQC *hotstuff.QuorumCertificate) *hotstuff.ConsensusState {
	return &hotstuff.ConsensusState{
		View:            headQC.View + 1,
		LockedQC:        *headQC,
		LastCommittedQC: *headQC,
	}
}

// p1SealHash is hotstuff's sealHash: the header hash with the trailing
// 96-byte seal removed from extraData.
func p1SealHash(h *block.Header) types.Hash {
	cpy := *h
	if len(cpy.Extra) > p1SealLen {
		cpy.Extra = cpy.Extra[:len(cpy.Extra)-p1SealLen]
	}
	cpy.ResetHashCache()
	return cpy.Hash()
}

// applyProfile1Config prepares cfg for profile-1 re-derivation: the target
// chain config is the profile-1 genesis's, the tree is QMDB (A5) and the
// committee evidence is always on with the genesis's committeePool (A19).
func applyProfile1Config(cfg *ConfigV2) error {
	g := cfg.Profile1Genesis
	if g == nil || g.Config == nil || !g.Config.IsProfile1() {
		return errors.New("replay profile 1: the genesis is not n42Profile 1")
	}
	if err := g.Config.ValidateProfile1(); err != nil {
		return fmt.Errorf("replay profile 1: %w", err)
	}
	if cfg.TreeType != "qmdb" {
		return fmt.Errorf("replay profile 1: --tree %q, profile 1 is QMDB only (A5)", cfg.TreeType)
	}
	if cfg.LeafJournal != "" || cfg.VerifyMPT {
		return errors.New("replay profile 1: --leaf-journal and --verify-mpt are not supported")
	}
	cfg.ChainConfig = g.Config
	cfg.BLSReseal = true
	return applyCommitteePoolConfig(cfg)
}

// applyCommitteePoolConfig takes the committee-evidence parameters from the
// target chain config's hotstuff.committeePool: the live node continues the
// replayed chain's evidence only with the same seed and sizes, so a flag that
// disagrees is refused. Without a pool in the config the flags stand.
func applyCommitteePoolConfig(cfg *ConfigV2) error {
	if !cfg.BLSReseal || cfg.ChainConfig == nil || cfg.ChainConfig.HotStuff == nil || cfg.ChainConfig.HotStuff.CommitteePool == nil {
		return nil
	}
	pool := cfg.ChainConfig.HotStuff.CommitteePool
	seed, err := hexutil.Decode(pool.SeedHex)
	if err != nil || len(seed) != 32 {
		return fmt.Errorf("committeePool.seedHex is not 32 bytes of hex: %q", pool.SeedHex)
	}
	var s [32]byte
	copy(s[:], seed)
	if cfg.BLSSeed != ([32]byte{}) && cfg.BLSSeed != s {
		return errors.New("--bls-seed differs from the target chain's committeePool.seedHex")
	}
	if cfg.BLSPoolSize != 0 && cfg.BLSPoolSize != pool.PoolSize ||
		cfg.BLSCommitteeSize != 0 && cfg.BLSCommitteeSize != pool.CommitteeSize ||
		cfg.BLSRampBlocks != 0 && cfg.BLSRampBlocks != pool.RampBlocks {
		return fmt.Errorf("--bls-pool-size/--bls-committee/--bls-ramp-blocks %d/%d/%d differ from the target committeePool %d/%d/%d",
			cfg.BLSPoolSize, cfg.BLSCommitteeSize, cfg.BLSRampBlocks, pool.PoolSize, pool.CommitteeSize, pool.RampBlocks)
	}
	cfg.BLSSeed, cfg.BLSPoolSize, cfg.BLSCommitteeSize, cfg.BLSRampBlocks = s, pool.PoolSize, pool.CommitteeSize, pool.RampBlocks
	return nil
}
