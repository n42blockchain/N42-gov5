package replay

import (
	"fmt"
	"runtime"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// BLSResealConfig parameterises the simulated mobile-voter BLS re-seal.
//
// The consensus proof for each block is a HotStuff-2 BLS aggregate QC produced
// by a CommitteeSize-member committee that is deterministically sampled from
// the active mobile-voter pool. The pool ramps from one full committee at
// genesis up to PoolSize over RampBlocks blocks (simulating network growth).
//
// IMPORTANT: the QC is written to the ConsensusEvidence MDBX table (keyed by
// block number), NOT into Header.Extra. The block header stays ETH-standard
// (Extra = 32B vanity) so block explorers parse it normally; the header commits
// to the evidence through ParentBeaconRoot = Blake3(parent ConsensusEvidence).
type BLSResealConfig struct {
	Seed          [32]byte // master seed; keys derived as blsKeygen(sha256(seed||i))
	PoolSize      int      // total mobile-voter pool (e.g. 200000)
	CommitteeSize int      // per-block committee (e.g. 512, ETH sync-committee reference)
	RampBlocks    uint64   // blocks over which the active pool ramps committee->PoolSize
}

// BLSResealer holds the derived key pool and produces a HotStuff-2 committee QC
// (as a rawdb.ConsensusEvidence) for each re-sealed block.
type BLSResealer struct {
	cfg  BLSResealConfig
	sks  []common.SecretKey
	pks  []common.PublicKey
	npar int
	jobs chan signJob // persistent signing worker pool (avoids per-block goroutine spawn)
}

// signJob signs members[lo:hi] over a precomputed message hash into sigs, then
// signals wg. precomp holds the message hashed to G2 once (shared, read-only);
// each member only does the scalar multiplication sk*H(msg), ~halving sign cost
// vs re-hashing the identical message per member.
type signJob struct {
	members []int
	precomp *bls.PrecomputedHash
	sigs    []common.Signature
	lo, hi  int
	wg      *sync.WaitGroup
}

// NewBLSResealer derives the full key pool from the master seed (parallel).
func NewBLSResealer(cfg BLSResealConfig) (*BLSResealer, error) {
	if cfg.PoolSize <= 0 || cfg.CommitteeSize <= 0 {
		return nil, fmt.Errorf("blsreseal: invalid pool/committee size")
	}
	if cfg.CommitteeSize > cfg.PoolSize {
		return nil, fmt.Errorf("blsreseal: committee %d > pool %d", cfg.CommitteeSize, cfg.PoolSize)
	}
	r := &BLSResealer{cfg: cfg, npar: runtime.GOMAXPROCS(0)}
	sks, pks, err := blspool.DeriveKeys(cfg.Seed, cfg.PoolSize, true)
	if err != nil {
		return nil, fmt.Errorf("blsreseal: %w", err)
	}
	r.sks, r.pks = sks, pks

	// Persistent signing worker pool: workers live for the whole run so we pay
	// no per-block goroutine spawn cost (the dominant overhead at 512 sigs/block
	// across millions of blocks).
	r.jobs = make(chan signJob, r.npar*2)
	for w := 0; w < r.npar; w++ {
		go func() {
			for j := range r.jobs {
				for i := j.lo; i < j.hi; i++ {
					j.sigs[i] = j.precomp.SignWith(r.sks[j.members[i]])
				}
				j.wg.Done()
			}
		}()
	}
	return r, nil
}

// PoolSize returns the configured total pool size.
func (r *BLSResealer) PoolSize() int { return r.cfg.PoolSize }

// CommitteeSize returns the configured per-block committee size.
func (r *BLSResealer) CommitteeSize() int { return r.cfg.CommitteeSize }

// ActivePool returns the number of mobile voters active at the given block:
// it ramps linearly from one full committee at genesis up to PoolSize over
// RampBlocks blocks, then stays at PoolSize.
func (r *BLSResealer) ActivePool(blockNum uint64) int {
	return blspool.ActivePool(blockNum, r.cfg.PoolSize, r.cfg.CommitteeSize, r.cfg.RampBlocks)
}

// committee deterministically samples CommitteeSize distinct validator indices
// from [0, active) using a partial Fisher-Yates shuffle seeded by (view,
// blockHash). The result is the ordered validator set for that view.
func (r *BLSResealer) committee(view hotstuff.ViewNumber, blockHash types.Hash, active int) []int {
	return blspool.Committee(uint64(view), blockHash, active, r.cfg.CommitteeSize)
}

// signMembers signs msg with every committee member's key, dispatching
// fixed-size chunks to the persistent worker pool.
func (r *BLSResealer) signMembers(members []int, msg []byte) []common.Signature {
	sigs := make([]common.Signature, len(members))
	// Hash the message to G2 once (the dominant ~half of blst sign cost); all
	// committee members sign the SAME message, so workers reuse this point and
	// only perform the scalar multiplication.
	precomp := bls.PrecomputeHash(msg)
	chunk := (len(members) + r.npar - 1) / r.npar
	if chunk < 1 {
		chunk = 1
	}
	var wg sync.WaitGroup
	for lo := 0; lo < len(members); lo += chunk {
		hi := lo + chunk
		if hi > len(members) {
			hi = len(members)
		}
		wg.Add(1)
		r.jobs <- signJob{members: members, precomp: precomp, sigs: sigs, lo: lo, hi: hi, wg: &wg}
	}
	wg.Wait()
	return sigs
}

// BuildCE produces the HotStuff-2 committee QC for a block as a
// rawdb.ConsensusEvidence. The committee is sampled from the active pool at the
// block's view, every member signs SigningMessage(view, blockHash) (matching
// the live engine's verify path: aggSig.Verify(AggregateMultiplePubkeys, msg)),
// and the aggregate plus signer bitmap are recorded. receiptRoot is recorded in
// the mobile sub-record so mobile parallel-verification attestation is visible.
func (r *BLSResealer) BuildCE(blockNum uint64, blockHash types.Hash, receiptRoot types.Hash) *rawdb.ConsensusEvidence {
	view := hotstuff.ViewNumber(blockNum)
	active := r.ActivePool(blockNum)
	members := r.committee(view, blockHash, active)
	msg := hotstuff.SigningMessage(view, blockHash)
	sigs := r.signMembers(members, msg)
	agg := bls.AggregateSignatures(sigs)

	ce := &rawdb.ConsensusEvidence{
		View:        uint64(view),
		BlockHash:   blockHash,
		SignerCount: uint16(len(members)),
	}
	copy(ce.AggregateSignature[:], agg.Marshal())
	ce.SignersPacked = make([]byte, (len(members)+7)/8)
	for i := range members {
		ce.SignersPacked[i/8] |= 1 << uint(i%8)
	}
	// Mobile parallel-verification layer: the committee members are mobile
	// devices sampled from the pool; record the attested receipt root and
	// participant bitmap so the mobile consensus contribution is explicit.
	ce.HasMobile = true
	ce.MobReceiptsRoot = receiptRoot
	ce.MobParticipantCount = uint16(len(members))
	ce.MobParticipantsPacked = ce.SignersPacked
	copy(ce.MobAggSignature[:], agg.Marshal())
	return ce
}

// VerifyCE re-derives the committee for the evidence and checks the aggregate
// signature exactly as the live HotStuff engine does
// (aggSig.Verify(AggregateMultiplePubkeys(signers), SigningMessage)).
func (r *BLSResealer) VerifyCE(ce *rawdb.ConsensusEvidence) (bool, error) {
	view := hotstuff.ViewNumber(ce.View)
	active := r.ActivePool(ce.View)
	members := r.committee(view, ce.BlockHash, active)
	if int(ce.SignerCount) != len(members) {
		return false, fmt.Errorf("signer count %d != committee %d", ce.SignerCount, len(members))
	}
	pubs := make([]common.PublicKey, 0, len(members))
	for i := range members {
		if i < len(ce.SignersPacked)*8 && ce.SignersPacked[i/8]&(1<<uint(i%8)) != 0 {
			pubs = append(pubs, r.pks[members[i]])
		}
	}
	aggSig, err := bls.SignatureFromBytes(ce.AggregateSignature[:])
	if err != nil {
		return false, err
	}
	aggPK := bls.AggregateMultiplePubkeys(pubs)
	msg := hotstuff.SigningMessage(view, ce.BlockHash)
	if !aggSig.Verify(aggPK, msg) {
		return false, fmt.Errorf("aggregate verify failed at view %d", view)
	}
	return true, nil
}
