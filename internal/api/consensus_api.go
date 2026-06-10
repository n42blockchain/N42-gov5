// Consensus evidence JSON-RPC API (namespace "n42").
//
// This is the N42 analogue of Ethereum's Beacon API block endpoints: it lets a
// block explorer fetch the per-block BLS consensus signature data (the HotStuff
// committee aggregate QC and the optional mobile-voting aggregate) that lives in
// the ConsensusEvidence table — data that is deliberately kept OUT of the
// ETH-standard block header so explorers parse blocks normally.

package api

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// ConsensusAPI exposes per-block consensus evidence (BLS QC + mobile votes) and,
// when the mobile-voter pool is configured, per-block committee membership.
type ConsensusAPI struct {
	api *API

	// Lazily-derived voter pool for committee resolution. Configured via env:
	//   N42_BLS_POOL_SEED   (32-byte hex master seed)
	//   N42_BLS_POOL_SIZE   (default 200000)
	//   N42_BLS_COMMITTEE   (default 512)
	//   N42_BLS_RAMP_BLOCKS (default 1000000)
	configOnce sync.Once
	poolOnce   sync.Once
	poolPks    []common.PublicKey
	poolErr    error
	poolSize   int
	committee  int
	rampBlocks uint64
}

// NewConsensusAPI builds the consensus evidence API service.
func NewConsensusAPI(api *API) *ConsensusAPI {
	return &ConsensusAPI{api: api}
}

// ConsensusEvidenceResult is the explorer-facing view of a block's consensus
// evidence. Byte fields are 0x-hex; the BLS aggregate is a 96-byte G2 signature
// (ETH-CL ciphersuite BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_).
type ConsensusEvidenceResult struct {
	BlockNumber        hexutil.Uint64 `json:"blockNumber"`
	View               hexutil.Uint64 `json:"view"`
	BlockHash          types.Hash     `json:"blockHash"`
	AggregateSignature hexutil.Bytes  `json:"aggregateSignature"`
	SignerCount        hexutil.Uint64 `json:"signerCount"`
	Signers            hexutil.Bytes  `json:"signers"` // committee participation bitmap (⌈n/8⌉ bytes)

	HasMobile                bool            `json:"hasMobile"`
	MobileReceiptsRoot       *types.Hash     `json:"mobileReceiptsRoot,omitempty"`
	MobileAggregateSignature hexutil.Bytes   `json:"mobileAggregateSignature,omitempty"`
	MobileParticipantCount   *hexutil.Uint64 `json:"mobileParticipantCount,omitempty"`
	MobileParticipants       hexutil.Bytes   `json:"mobileParticipants,omitempty"`
	MobileCreatedAtMs        *hexutil.Uint64 `json:"mobileCreatedAtMs,omitempty"`
}

// GetConsensusEvidence returns the consensus evidence (BLS committee QC and
// optional mobile-voting aggregate) for the given block. Accepts a block number
// or the "latest"/"earliest" tags.
func (c *ConsensusAPI) GetConsensusEvidence(ctx context.Context, blockNr jsonrpc.BlockNumber) (*ConsensusEvidenceResult, error) {
	num, err := c.resolveBlockNumber(blockNr)
	if err != nil {
		return nil, err
	}

	tx, err := c.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ce, err := rawdb.ReadConsensusEvidence(tx, num)
	if err != nil {
		return nil, err
	}
	if ce == nil {
		return nil, nil // no evidence recorded for this block (e.g. genesis)
	}

	res := &ConsensusEvidenceResult{
		BlockNumber:        hexutil.Uint64(num),
		View:               hexutil.Uint64(ce.View),
		BlockHash:          ce.BlockHash,
		AggregateSignature: append(hexutil.Bytes{}, ce.AggregateSignature[:]...),
		SignerCount:        hexutil.Uint64(ce.SignerCount),
		Signers:            append(hexutil.Bytes{}, ce.SignersPacked...),
		HasMobile:          ce.HasMobile,
	}
	if ce.HasMobile {
		root := ce.MobReceiptsRoot
		res.MobileReceiptsRoot = &root
		res.MobileAggregateSignature = append(hexutil.Bytes{}, ce.MobAggSignature[:]...)
		mpc := hexutil.Uint64(ce.MobParticipantCount)
		res.MobileParticipantCount = &mpc
		res.MobileParticipants = append(hexutil.Bytes{}, ce.MobParticipantsPacked...)
		ms := hexutil.Uint64(ce.MobCreatedAtMs)
		res.MobileCreatedAtMs = &ms
	}
	return res, nil
}

// CommitteeMember is one validator in a block's committee.
type CommitteeMember struct {
	Index  hexutil.Uint64 `json:"index"`  // pool index
	PubKey hexutil.Bytes  `json:"pubkey"` // 48-byte BLS12-381 G1 public key
	Signed bool           `json:"signed"` // whether this member's signature is in the QC
}

// CommitteeResult is the committee that finalised a block, the N42 analogue of
// Ethereum's sync-committee endpoint.
type CommitteeResult struct {
	BlockNumber    hexutil.Uint64    `json:"blockNumber"`
	View           hexutil.Uint64    `json:"view"`
	BlockHash      types.Hash        `json:"blockHash"`
	ActivePoolSize hexutil.Uint64    `json:"activePoolSize"`
	CommitteeSize  hexutil.Uint64    `json:"committeeSize"`
	SignedCount    hexutil.Uint64    `json:"signedCount"`
	Members        []CommitteeMember `json:"members"`
}

// GetCommittee resolves the committee membership (public keys + who signed) that
// produced a block's QC, re-deriving the committee from the configured voter
// pool. Requires N42_BLS_POOL_SEED (and matching size/committee/ramp) to be set.
func (c *ConsensusAPI) GetCommittee(ctx context.Context, blockNr jsonrpc.BlockNumber) (*CommitteeResult, error) {
	num, err := c.resolveBlockNumber(blockNr)
	if err != nil {
		return nil, err
	}
	if err := c.ensurePool(); err != nil {
		return nil, err
	}

	tx, err := c.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ce, err := rawdb.ReadConsensusEvidence(tx, num)
	if err != nil {
		return nil, err
	}
	if ce == nil {
		return nil, nil
	}

	active := blspool.ActivePool(ce.View, c.poolSize, c.committee, c.rampBlocks)
	members := blspool.Committee(ce.View, ce.BlockHash, active, c.committee)

	res := &CommitteeResult{
		BlockNumber:    hexutil.Uint64(num),
		View:           hexutil.Uint64(ce.View),
		BlockHash:      ce.BlockHash,
		ActivePoolSize: hexutil.Uint64(active),
		CommitteeSize:  hexutil.Uint64(len(members)),
		Members:        make([]CommitteeMember, 0, len(members)),
	}
	var signed uint64
	for i, idx := range members {
		isSigned := i < len(ce.SignersPacked)*8 && ce.SignersPacked[i/8]&(1<<uint(i%8)) != 0
		if isSigned {
			signed++
		}
		var pk hexutil.Bytes
		if idx >= 0 && idx < len(c.poolPks) {
			pk = append(hexutil.Bytes{}, c.poolPks[idx].Marshal()...)
		}
		res.Members = append(res.Members, CommitteeMember{
			Index:  hexutil.Uint64(idx),
			PubKey: pk,
			Signed: isSigned,
		})
	}
	res.SignedCount = hexutil.Uint64(signed)
	return res, nil
}

// ensureConfig reads the (cheap) pool sizing parameters from env. These are
// enough to resolve pool/committee sizes and committee membership indices
// without deriving any keys.
func (c *ConsensusAPI) ensureConfig() {
	c.configOnce.Do(func() {
		c.poolSize = envInt("N42_BLS_POOL_SIZE", 200000)
		c.committee = envInt("N42_BLS_COMMITTEE", 512)
		c.rampBlocks = uint64(envInt("N42_BLS_RAMP_BLOCKS", 1000000))
	})
}

// ensurePool lazily derives the voter pool public keys from the env config.
// Required only for endpoints that return public keys.
func (c *ConsensusAPI) ensurePool() error {
	c.ensureConfig()
	c.poolOnce.Do(func() {
		seedHex := strings.TrimPrefix(os.Getenv("N42_BLS_POOL_SEED"), "0x")
		if seedHex == "" {
			c.poolErr = fmt.Errorf("public-key resolution disabled: set N42_BLS_POOL_SEED")
			return
		}
		seedBytes, err := hex.DecodeString(seedHex)
		if err != nil || len(seedBytes) != 32 {
			c.poolErr = fmt.Errorf("N42_BLS_POOL_SEED must be 32-byte hex")
			return
		}
		var seed [32]byte
		copy(seed[:], seedBytes)
		_, pks, derr := blspool.DeriveKeys(seed, c.poolSize, false)
		if derr != nil {
			c.poolErr = derr
			return
		}
		c.poolPks = pks
	})
	return c.poolErr
}

// ValidatorPoolResult describes the mobile-voter pool sizing at a block.
type ValidatorPoolResult struct {
	BlockNumber    hexutil.Uint64 `json:"blockNumber"`
	ActivePoolSize hexutil.Uint64 `json:"activePoolSize"` // voters eligible at this block
	CommitteeSize  hexutil.Uint64 `json:"committeeSize"`  // per-block committee
	TotalPoolSize  hexutil.Uint64 `json:"totalPoolSize"`  // pool size once fully ramped
	RampBlocks     hexutil.Uint64 `json:"rampBlocks"`     // ramp duration
	FullyRamped    bool           `json:"fullyRamped"`
}

// GetValidatorPool returns the voter-pool sizing at a block (no keys needed).
func (c *ConsensusAPI) GetValidatorPool(ctx context.Context, blockNr jsonrpc.BlockNumber) (*ValidatorPoolResult, error) {
	num, err := c.resolveBlockNumber(blockNr)
	if err != nil {
		return nil, err
	}
	c.ensureConfig()
	active := blspool.ActivePool(num, c.poolSize, c.committee, c.rampBlocks)
	return &ValidatorPoolResult{
		BlockNumber:    hexutil.Uint64(num),
		ActivePoolSize: hexutil.Uint64(active),
		CommitteeSize:  hexutil.Uint64(c.committee),
		TotalPoolSize:  hexutil.Uint64(c.poolSize),
		RampBlocks:     hexutil.Uint64(c.rampBlocks),
		FullyRamped:    active >= c.poolSize,
	}, nil
}

// ValidatorInfo is a single pool member's identity.
type ValidatorInfo struct {
	Index   hexutil.Uint64 `json:"index"`
	PubKey  hexutil.Bytes  `json:"pubkey"`  // 48-byte BLS12-381 G1 public key
	Address types.Address  `json:"address"` // keccak256(pubkey)[12:]
}

// GetValidator returns the public key and derived address of a pool member.
func (c *ConsensusAPI) GetValidator(ctx context.Context, index hexutil.Uint64) (*ValidatorInfo, error) {
	if err := c.ensurePool(); err != nil {
		return nil, err
	}
	i := int(index)
	if i < 0 || i >= len(c.poolPks) {
		return nil, fmt.Errorf("validator index %d out of range [0,%d)", i, len(c.poolPks))
	}
	pub := c.poolPks[i].Marshal()
	var addr types.Address
	copy(addr[:], crypto.Keccak256(pub)[12:])
	return &ValidatorInfo{
		Index:   index,
		PubKey:  append(hexutil.Bytes{}, pub...),
		Address: addr,
	}, nil
}

// ValidatorDuty records that a validator was in a block's committee.
type ValidatorDuty struct {
	BlockNumber hexutil.Uint64 `json:"blockNumber"`
	View        hexutil.Uint64 `json:"view"`
	Position    hexutil.Uint64 `json:"position"` // index within the committee
	Signed      bool           `json:"signed"`
}

const maxDutiesRange = 50000

// GetValidatorDuties scans a bounded block range and reports the blocks in which
// the validator was sampled into the committee, and whether it signed. The range
// is capped at maxDutiesRange blocks. No public keys are needed.
func (c *ConsensusAPI) GetValidatorDuties(ctx context.Context, index hexutil.Uint64, fromBlock, toBlock hexutil.Uint64) ([]ValidatorDuty, error) {
	c.ensureConfig()
	from, to := uint64(fromBlock), uint64(toBlock)
	if to < from {
		return nil, fmt.Errorf("toBlock < fromBlock")
	}
	if to-from+1 > maxDutiesRange {
		return nil, fmt.Errorf("range too large: %d blocks (max %d)", to-from+1, maxDutiesRange)
	}
	target := int(index)

	tx, err := c.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	duties := make([]ValidatorDuty, 0, 64)
	for num := from; num <= to; num++ {
		ce, err := rawdb.ReadConsensusEvidence(tx, num)
		if err != nil || ce == nil {
			continue
		}
		active := blspool.ActivePool(ce.View, c.poolSize, c.committee, c.rampBlocks)
		members := blspool.Committee(ce.View, ce.BlockHash, active, c.committee)
		for pos, idx := range members {
			if idx == target {
				signed := pos < len(ce.SignersPacked)*8 && ce.SignersPacked[pos/8]&(1<<uint(pos%8)) != 0
				duties = append(duties, ValidatorDuty{
					BlockNumber: hexutil.Uint64(num),
					View:        hexutil.Uint64(ce.View),
					Position:    hexutil.Uint64(pos),
					Signed:      signed,
				})
				break
			}
		}
	}
	return duties, nil
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// StateProofInfo describes how eth_getProof results are encoded and how to
// verify them — so a client can detect a QMDB-backed chain and route each proof
// blob to qmdb.VerifyEncodedProof instead of an MPT verifier.
type StateProofInfo struct {
	Backend               string `json:"backend"`               // e.g. "qmdb", "jmt", "ethereum-mpt", "hash-only"
	ProofRootScheme       string `json:"proofRootScheme"`       // commitment behind the proof
	Semantics             string `json:"semantics"`             // EIP-1186 conformance
	StorageHash           string `json:"storageHash"`           // how AccountResult.storageHash is computed
	SupportsHistorical    bool   `json:"supportsHistorical"`    // proofs for non-latest blocks
	HeaderStateRootScheme string `json:"headerStateRootScheme"` // header.stateRoot semantics
}

// GetStateProofInfo reports the proof backend serving eth_getProof. For a QMDB
// chain Backend == "qmdb": each accountProof/storageProof element is a single
// hex blob to decode with qmdb.UnmarshalProof and check with
// qmdb.VerifyEncodedProof against the block's stateRoot.
func (c *ConsensusAPI) GetStateProofInfo(ctx context.Context) (*StateProofInfo, error) {
	bc, ok := c.api.BlockChain().(*internal.BlockChain)
	if !ok {
		return nil, fmt.Errorf("state proof info unavailable: concrete blockchain required")
	}
	d := bc.StateProofDescriptor()
	return &StateProofInfo{
		Backend:               string(d.Backend),
		ProofRootScheme:       string(d.ProofRootScheme),
		Semantics:             string(d.Semantics),
		StorageHash:           string(d.StorageHash),
		SupportsHistorical:    d.SupportsHistorical,
		HeaderStateRootScheme: string(d.HeaderStateRootScheme),
	}, nil
}

// livePool resolves the live committee pool wired into the running chain (the
// same instance that stamps evidence onto produced blocks), or an error when no
// pool is configured.
func (c *ConsensusAPI) livePool() (*blspool.Pool, error) {
	bc, ok := c.api.BlockChain().(*internal.BlockChain)
	if !ok {
		return nil, fmt.Errorf("committee hand-over unavailable: concrete blockchain required")
	}
	cp := bc.CommitteePool()
	if cp == nil {
		return nil, fmt.Errorf("committee pool not enabled (set hotStuff.committeePool)")
	}
	pool, ok := cp.(*blspool.Pool)
	if !ok {
		return nil, fmt.Errorf("committee pool type mismatch")
	}
	return pool, nil
}

// RegisterCommitteeValidator hands committee-pool slot over to a real validator.
// proofSig must be the validator's BLS signature over
// RegistrationMessage(slot, pubkey) — proof that it controls the key. Requires
// hand-over to be enabled (hotStuff.committeePool.allowHandover).
func (c *ConsensusAPI) RegisterCommitteeValidator(ctx context.Context, slot hexutil.Uint64, pubkey, proofSig hexutil.Bytes) (bool, error) {
	pool, err := c.livePool()
	if err != nil {
		return false, err
	}
	if err := pool.RegisterValidator(int(slot), pubkey, proofSig); err != nil {
		return false, err
	}
	return true, nil
}

// SubmitCommitteeSignature submits a real validator's partial signature for a
// block. The signature must verify against the slot's registered public key over
// SigningMessage(blockNum, blockHash); only committee members are accepted. The
// signature is folded into the block's consensus evidence when it is produced.
func (c *ConsensusAPI) SubmitCommitteeSignature(ctx context.Context, blockNum hexutil.Uint64, blockHash types.Hash, slot hexutil.Uint64, sig hexutil.Bytes) (bool, error) {
	pool, err := c.livePool()
	if err != nil {
		return false, err
	}
	if err := pool.SubmitSignature(uint64(blockNum), blockHash, int(slot), sig); err != nil {
		return false, err
	}
	return true, nil
}

func (c *ConsensusAPI) resolveBlockNumber(blockNr jsonrpc.BlockNumber) (uint64, error) {
	switch {
	case blockNr == jsonrpc.LatestBlockNumber || blockNr == jsonrpc.PendingBlockNumber:
		cur := c.api.BlockChain().CurrentBlock()
		if cur == nil {
			return 0, fmt.Errorf("current block unavailable")
		}
		return cur.Number64().Uint64(), nil
	case blockNr == jsonrpc.EarliestBlockNumber:
		return 0, nil
	case blockNr < 0:
		return 0, fmt.Errorf("invalid block number %d", blockNr)
	default:
		return uint64(blockNr.Int64()), nil
	}
}
