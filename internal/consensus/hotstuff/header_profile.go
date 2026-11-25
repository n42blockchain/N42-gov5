// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Header profile of the N42 mixed production standard, profile 1 (section
// 2(b) with amendments AM3, AM4, AM5): the static header checks applied to
// every non-genesis header of a chain whose genesis says "n42Profile": 1.
// Chains outside the profile never reach this file.

package hotstuff

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/misc"
)

// maxProfile1ExtraLen bounds a profile-1 header's extraData (B1).
const maxProfile1ExtraLen = 4096

// maxProfile1AncestorWalk bounds the walk that proves a header QC names an
// ancestor. A QC further back than this is not walked (see checkProfile1QC).
const maxProfile1AncestorWalk = 4096

// Profile-1 header refusals. Every error returned by the profile-1 checks
// wraps exactly one of these, so a caller (and vector V1) can name the rule.
var (
	// ErrProfile1Extra: extraData is not "N42H" || view u64 LE || [QC] ||
	// seal(96), or is longer than 4096 bytes (B1).
	ErrProfile1Extra = errors.New("profile 1: invalid header extraData")
	// ErrProfile1QC: the embedded QC is not a commit-domain QC of the genesis
	// set, does not precede the header's view, or names a non-ancestor (B1, E13).
	ErrProfile1QC = errors.New("profile 1: invalid header QC")
	// ErrProfile1Seal: the trailing 96 bytes of extraData are not the
	// proposer's BLS signature (H2-v4 POP DST) over sealHash (B2, AM4).
	ErrProfile1Seal = errors.New("profile 1: invalid seal")
	// ErrProfile1Proposer: coinbase is not the leader of the header's view
	// (B2, B3).
	ErrProfile1Proposer = errors.New("profile 1: coinbase is not the leader of the view")
	// ErrProfile1Difficulty, ErrProfile1Nonce, ErrProfile1MixHash and
	// ErrProfile1OmmersHash: a constant field is not zero (B3). The
	// empty-list ommers hash 0x1dcc4de8... is refused like any other value.
	ErrProfile1Difficulty = errors.New("profile 1: difficulty is not zero")
	ErrProfile1Nonce      = errors.New("profile 1: nonce is not zero")
	ErrProfile1MixHash    = errors.New("profile 1: mixHash is not zero")
	ErrProfile1OmmersHash = errors.New("profile 1: ommersHash is not zero")
	// ErrProfile1TimeBeforeParent: header.time < parent.time (B4; equal
	// timestamps are valid, O7). Checked on import and at vote time.
	ErrProfile1TimeBeforeParent = errors.New("profile 1: timestamp before parent")
	// ErrProfile1TimeFuture: header.time > floor(now) + 2 (B4). Checked only
	// at vote time (StaticHeaderCheck), never on import of a committed block.
	ErrProfile1TimeFuture = errors.New("profile 1: timestamp too far ahead of the local clock")
)

// Profile-1 field refusals (B6, B8, B9, B12).
var (
	// ErrProfile1WithdrawalsRoot: withdrawalsRoot (the rewards commitment) is
	// absent. Its value is checked against the body after execution (B6).
	ErrProfile1WithdrawalsRoot = errors.New("profile 1: withdrawalsRoot is absent")
	// ErrProfile1RequestsHash: requestsHash is absent (B8). Its value is a
	// deferred field (AM25, A10): the parent's executed EIP-7685 requests
	// hash, checked against this node's execution of the parent.
	ErrProfile1RequestsHash = errors.New("profile 1: requestsHash is absent")
	// ErrProfile1BlobGas: blobGasUsed or excessBlobGas absent or non-zero (B9).
	ErrProfile1BlobGas = errors.New("profile 1: blob gas fields are not zero")
	// ErrProfile1Element22: header element 22 (block access list hash) is
	// present; profile 1 writes the empty 0x80 gap (B12, O6).
	ErrProfile1Element22 = errors.New("profile 1: element 22 is not the empty gap")
)

// Profile-1 gas refusals (A23, B14).
var (
	// ErrProfile1GasLimit: gasLimit differs from config.hotstuff.gasLimit (A23).
	ErrProfile1GasLimit = errors.New("profile 1: gasLimit is not hotstuff.gasLimit")
	// ErrProfile1BaseFee: baseFee is not EIP-1559's value from the parent
	// header as written (B14).
	ErrProfile1BaseFee = errors.New("profile 1: baseFee is not the EIP-1559 value of the parent")
)

// Profile1RequestsHash is EIP-7685's hash of no requests, sha256(""): the
// requestsHash of the profile-1 genesis and of block 1 (AM24, AM25), and of
// every header whose parent produced no requests. A header carries its
// parent's executed requests hash (AM25), so other values are valid too.
var Profile1RequestsHash = types.Hash(sha256.Sum256(nil))

// profile1ClockSkew is B4's vote-time bound in seconds above floor(now).
const profile1ClockSkew = 2

// profile1Validators is the profile-1 validator set: the genesis set, fixed
// for the chain's life (AM5). Parsed once from the chain config so the header
// checks work on nodes that run no consensus engine.
type profile1Validators struct {
	once sync.Once
	set  *ValidatorSet
	err  error
}

// profile1Set returns the chain's fixed validator set.
func (h *HotStuff) profile1Set() (*ValidatorSet, error) {
	h.p1Validators.once.Do(func() {
		validators, err := validatorsFromConfig(h.config)
		if err != nil {
			h.p1Validators.err = err
			return
		}
		n := uint32(len(validators))
		h.p1Validators.set = NewValidatorSet(validators, (n-1)/3)
	})
	return h.p1Validators.set, h.p1Validators.err
}

// Profile1ValidatorSet returns the fixed validator set of a profile-1 chain
// (AM5), parsed from the chain config, so nodes that run no consensus engine
// (non-validators) can still authenticate validator-signed messages. It is
// nil on a chain outside profile 1, whose set may rotate at epoch boundaries.
func (h *HotStuff) Profile1ValidatorSet() *ValidatorSet {
	if !h.chainConfig.IsProfile1() {
		return nil
	}
	vs, err := h.profile1Set()
	if err != nil {
		return nil
	}
	return vs
}

// decodeProfile1Extra decodes extraData in the one layout of B1 and nothing
// else: "N42H" || view u64 LE || QC || seal(96), at most 4096 bytes. The QC is
// absent as zero bytes (no other spelling of an empty QC) and must be the
// canonical SSZ encoding of what it decodes to.
func decodeProfile1Extra(extra []byte) (ViewNumber, *QuorumCertificate, []byte, error) {
	if len(extra) > maxProfile1ExtraLen {
		return 0, nil, nil, fmt.Errorf("%w: %d bytes, at most %d", ErrProfile1Extra, len(extra), maxProfile1ExtraLen)
	}
	if len(extra) < extraMinLen+extraSealLen {
		return 0, nil, nil, fmt.Errorf("%w: %d bytes, at least %d (magic, view, seal)", ErrProfile1Extra, len(extra), extraMinLen+extraSealLen)
	}
	view, err := ExtractViewFromExtra(extra)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: %v", ErrProfile1Extra, err)
	}
	payload := extra[extraMinLen : len(extra)-extraSealLen]
	seal := extra[len(extra)-extraSealLen:]
	if len(payload) == 0 {
		return view, nil, seal, nil
	}
	qc, err := decodeQC(payload)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: QC does not decode: %v", ErrProfile1Extra, err)
	}
	if isEmptyHeaderQC(qc) {
		return 0, nil, nil, fmt.Errorf("%w: an absent QC is zero bytes, not an encoded empty QC", ErrProfile1Extra)
	}
	enc, err := encodeQC(qc)
	if err != nil || !bytes.Equal(enc, payload) {
		return 0, nil, nil, fmt.Errorf("%w: QC is not canonically encoded", ErrProfile1Extra)
	}
	return view, qc, seal, nil
}

// lookupProfile1Header resolves a header by hash from the in-batch index of a
// range import, then the chain.
func lookupProfile1Header(chain consensus.ChainHeaderReader, batch map[types.Hash]*block.Header, h types.Hash) *block.Header {
	if hd := batch[h]; hd != nil {
		return hd
	}
	if chain == nil {
		return nil
	}
	ih, err := chain.GetHeaderByHash(h)
	if err != nil || ih == nil {
		return nil
	}
	hd, _ := ih.(*block.Header)
	return hd
}

// checkProfile1QC applies B1/E13 to the QC embedded in a header of view
// view: it precedes the view, it verifies in the commit domain (H2-v4, zero
// changesHash) against the genesis set, and, when the QC's block is known
// locally, that block is an ancestor of the header.
func (h *HotStuff) checkProfile1QC(chain consensus.ChainHeaderReader, header, parent *block.Header, view ViewNumber, qc *QuorumCertificate, batch map[types.Hash]*block.Header) error {
	if qc == nil {
		return nil
	}
	if qc.View >= view {
		return fmt.Errorf("%w: QC view %d is not below header view %d", ErrProfile1QC, qc.View, view)
	}
	identity, err := h.profile1Identity(chain, batch)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProfile1QC, err)
	}
	vs, err := h.profile1Set()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProfile1QC, err)
	}
	msg := H2V4CommitSigningMessage(identity, qc.View, qc.BlockHash, types.Hash{})
	if err := verifyAggregateSignature(qc, vs, msg, "CommitQC"); err != nil {
		return fmt.Errorf("%w: %v", ErrProfile1QC, err)
	}

	// Ancestry: the QC's block sits below the header on the header's own
	// branch. Unknown blocks (a QC naming history this node has not
	// stored) cannot be walked and are accepted on the signature alone.
	target := lookupProfile1Header(chain, batch, qc.BlockHash)
	if target == nil || target.Number == nil {
		return nil
	}
	tn := target.Number.Uint64()
	if tn >= header.Number.Uint64() {
		return fmt.Errorf("%w: QC block %d is not below header %d", ErrProfile1QC, tn, header.Number.Uint64())
	}
	cur := parent
	for steps := 0; steps <= maxProfile1AncestorWalk; steps++ {
		if cur == nil || cur.Number == nil {
			return nil // a gap in local history: not decidable here
		}
		if cur.Number.Uint64() == tn {
			if cur.Hash() != qc.BlockHash {
				return fmt.Errorf("%w: QC block %x is not an ancestor", ErrProfile1QC, qc.BlockHash[:8])
			}
			return nil
		}
		if cur.Number.Uint64() < tn {
			return fmt.Errorf("%w: QC block %x is not an ancestor", ErrProfile1QC, qc.BlockHash[:8])
		}
		cur = lookupProfile1Header(chain, batch, cur.ParentHash)
	}
	return nil
}

// profile1Identity is the chain-bound H2-v4 identity (chainId, genesisHash)
// of header QCs, resolved from the chain config and the stored genesis.
func (h *HotStuff) profile1Identity(chain consensus.ChainHeaderReader, batch map[types.Hash]*block.Header) (H2V4ChainIdentity, error) {
	h.lock.RLock()
	id := h.p1Identity
	h.lock.RUnlock()
	if id != nil {
		return *id, nil
	}
	if h.chainConfig == nil || h.chainConfig.ChainID == nil || !h.chainConfig.ChainID.IsUint64() {
		return H2V4ChainIdentity{}, errors.New("chain id unavailable")
	}
	if chain == nil {
		return H2V4ChainIdentity{}, errors.New("genesis header unavailable")
	}
	g := chain.GetHeaderByNumber(uint256.NewInt(0))
	gh, ok := g.(*block.Header)
	if g == nil || !ok || gh == nil {
		return H2V4ChainIdentity{}, errors.New("genesis header unavailable")
	}
	resolved := H2V4ChainIdentity{ChainID: h.chainConfig.ChainID.Uint64(), GenesisHash: gh.Hash()}
	h.lock.Lock()
	h.p1Identity = &resolved
	h.lock.Unlock()
	return resolved, nil
}

// checkProfile1Header applies the static header rules of profile 1 that need
// only the header and its parent (the chain is used for the QC's ancestry and
// the genesis identity). The rules shared with legacy chains (B5, B10, B11,
// B13) stay in verifyHeaderWithBatch.
func (h *HotStuff) checkProfile1Header(chain consensus.ChainHeaderReader, header, parent *block.Header, batch map[types.Hash]*block.Header) error {
	view, qc, seal, err := decodeProfile1Extra(header.Extra)
	if err != nil {
		return err
	}
	if err := checkProfile1Constants(header); err != nil {
		return err
	}
	if err := checkProfile1Fields(header); err != nil {
		return err
	}
	if err := h.checkProfile1Gas(header, parent); err != nil {
		return err
	}
	if header.Time < parent.Time {
		return fmt.Errorf("%w: %d < parent %d", ErrProfile1TimeBeforeParent, header.Time, parent.Time)
	}
	if err := h.checkProfile1QC(chain, header, parent, view, qc, batch); err != nil {
		return err
	}
	return h.verifyProfile1Seal(header, view, seal)
}

// verifyProfile1Seal checks B2 (AM4): the proposer is the leader of the
// header's view under the chain's tenure, the coinbase is its address, and
// seal is its BLS signature over sealHash (keccak256 of the header RLP with
// the last 96 extraData bytes removed). A verified header hash is cached:
// the hash commits to the coinbase and the seal.
func (h *HotStuff) verifyProfile1Seal(header *block.Header, view ViewNumber, seal []byte) error {
	vs, err := h.profile1Set()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProfile1Seal, err)
	}
	var tenure uint64 = 1
	if h.config != nil {
		tenure = max(h.config.LeaderTenure, 1)
	}
	leader := LeaderForViewWithTenure(view, vs, tenure)
	addr, err := vs.GetAddress(leader)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProfile1Proposer, err)
	}
	if header.Coinbase != addr {
		return fmt.Errorf("%w: coinbase %s, leader of view %d is %s", ErrProfile1Proposer, header.Coinbase.Hex(), view, addr.Hex())
	}
	key := header.Hash()
	if h.signatures != nil {
		if v, ok := h.signatures.Get(key); ok && v == addr {
			return nil
		}
	}
	pk, err := vs.GetPublicKey(leader)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProfile1Seal, err)
	}
	sig, err := bls.SignatureFromBytes(seal)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProfile1Seal, err)
	}
	digest := sealHash(header)
	notePairingCheck()
	if !sig.Verify(pk, digest[:]) {
		return fmt.Errorf("%w: not the leader's signature over sealHash %s", ErrProfile1Seal, digest.Hex())
	}
	if h.signatures != nil {
		h.signatures.Add(key, addr)
	}
	return nil
}

// checkProfile1Constants applies B3: difficulty, nonce, mixHash and
// ommersHash are zero. The coinbase half of B3 is checked with the seal.
// gov5 block bodies carry no ommers, so "no ommers" holds by construction.
func checkProfile1Constants(header *block.Header) error {
	switch {
	case header.Difficulty != nil && !header.Difficulty.IsZero():
		return fmt.Errorf("%w: %s", ErrProfile1Difficulty, header.Difficulty)
	case header.Nonce != (block.BlockNonce{}):
		return fmt.Errorf("%w: %x", ErrProfile1Nonce, header.Nonce[:])
	case header.MixDigest != (types.Hash{}):
		return fmt.Errorf("%w: %s", ErrProfile1MixHash, header.MixDigest.Hex())
	case header.UncleHash != (types.Hash{}):
		return fmt.Errorf("%w: %s", ErrProfile1OmmersHash, header.UncleHash.Hex())
	}
	return nil
}

// StaticHeaderCheck is the header half of the Round-1 vote gate (A13, E6,
// AM19): every import-time header rule of VerifyHeader, which needs the
// parent header, plus on profile 1 the vote-time clock bound of B4,
// header.time <= floor(now) + 2. The clock bound is deliberately absent from
// VerifyHeader: importing a committed block must not depend on the clock.
func (h *HotStuff) StaticHeaderCheck(chain consensus.ChainHeaderReader, header *block.Header, now time.Time) error {
	if err := h.verifyHeaderWithBatch(chain, header, nil); err != nil {
		return err
	}
	if h.chainConfig.IsProfile1() {
		return checkProfile1Clock(header, now)
	}
	return nil
}

// checkProfile1Clock applies B4's upper bound against the local clock now.
func checkProfile1Clock(header *block.Header, now time.Time) error {
	secs := now.Unix()
	if secs < 0 {
		secs = 0
	}
	if limit := uint64(secs) + profile1ClockSkew; header.Time > limit {
		return fmt.Errorf("%w: %d > floor(now) + %d = %d", ErrProfile1TimeFuture, header.Time, profile1ClockSkew, limit)
	}
	return nil
}

// checkProfile1Fields applies the fork-field rules B6 (presence), B8
// (presence; the value is deferred, AM25), B9 and B12.
func checkProfile1Fields(header *block.Header) error {
	switch {
	case header.WithdrawalsHash == nil:
		return ErrProfile1WithdrawalsRoot
	case header.RequestsHash == nil:
		return ErrProfile1RequestsHash
	case header.BlobGasUsed == nil || header.ExcessBlobGas == nil:
		return fmt.Errorf("%w: absent", ErrProfile1BlobGas)
	case *header.BlobGasUsed != 0 || *header.ExcessBlobGas != 0:
		return fmt.Errorf("%w: blobGasUsed %d, excessBlobGas %d", ErrProfile1BlobGas, *header.BlobGasUsed, *header.ExcessBlobGas)
	case header.BlockAccessListHash != nil:
		return fmt.Errorf("%w: %s", ErrProfile1Element22, header.BlockAccessListHash.Hex())
	}
	return nil
}

// checkProfile1Gas applies A23 (gasLimit equals hotstuff.gasLimit, no ramp)
// and B14 (baseFee is EIP-1559's value from the parent header, elasticity 2,
// denominator 8, with the parent's gasUsed field as written, which under
// deferred execution is the grandparent's execution).
func (h *HotStuff) checkProfile1Gas(header, parent *block.Header) error {
	var want uint64
	if h.chainConfig != nil && h.chainConfig.HotStuff != nil {
		want = h.chainConfig.HotStuff.GasLimit
	}
	if want == 0 || header.GasLimit != want {
		return fmt.Errorf("%w: %d, want %d", ErrProfile1GasLimit, header.GasLimit, want)
	}
	if header.BaseFee == nil {
		return fmt.Errorf("%w: absent", ErrProfile1BaseFee)
	}
	if expected := misc.CalcBaseFee(h.chainConfig, parent); header.BaseFee.ToBig().Cmp(expected) != 0 {
		return fmt.Errorf("%w: %s, want %s (parent baseFee %s, gasUsed %d, gasLimit %d)",
			ErrProfile1BaseFee, header.BaseFee, expected, parent.BaseFee, parent.GasUsed, parent.GasLimit)
	}
	return nil
}

// maxProfile1Stamps bounds the per-(view, parent) timestamp cache.
const maxProfile1Stamps = 64

type profile1StampKey struct {
	view   ViewNumber
	parent types.Hash
}

// profile1Stamps remembers the timestamp stamped for each (view, parent) a
// leader built on.
type profile1Stamps struct {
	mu sync.Mutex
	m  map[profile1StampKey]uint64
}

// profile1Stamp is the leader's B4 timestamp, max(parent.time, floor(now)),
// fixed the first time it is computed for (view, parent): a rebuild of the
// same view on the same parent re-stamps the same value, so the leader still
// produces one block per view (F6) even though the clock moved.
func (h *HotStuff) profile1Stamp(view ViewNumber, parent *block.Header) uint64 {
	key := profile1StampKey{view: view, parent: parent.Hash()}
	st := &h.p1Stamps
	st.mu.Lock()
	defer st.mu.Unlock()
	if t, ok := st.m[key]; ok {
		return t
	}
	clock := h.now
	if clock == nil {
		clock = time.Now
	}
	t := parent.Time
	if secs := clock().Unix(); secs > 0 && uint64(secs) > t {
		t = uint64(secs)
	}
	if st.m == nil {
		st.m = make(map[profile1StampKey]uint64)
	}
	if len(st.m) >= maxProfile1Stamps {
		for k := range st.m {
			if k.view < view {
				delete(st.m, k)
			}
		}
		if len(st.m) >= maxProfile1Stamps {
			clear(st.m)
		}
	}
	st.m[key] = t
	return t
}
