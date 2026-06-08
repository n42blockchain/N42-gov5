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
	"github.com/n42blockchain/N42/crypto/bls/common"
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

// ensurePool lazily derives the voter pool public keys from the env config.
func (c *ConsensusAPI) ensurePool() error {
	c.poolOnce.Do(func() {
		seedHex := strings.TrimPrefix(os.Getenv("N42_BLS_POOL_SEED"), "0x")
		if seedHex == "" {
			c.poolErr = fmt.Errorf("committee resolution disabled: set N42_BLS_POOL_SEED")
			return
		}
		seedBytes, err := hex.DecodeString(seedHex)
		if err != nil || len(seedBytes) != 32 {
			c.poolErr = fmt.Errorf("N42_BLS_POOL_SEED must be 32-byte hex")
			return
		}
		var seed [32]byte
		copy(seed[:], seedBytes)
		c.poolSize = envInt("N42_BLS_POOL_SIZE", 200000)
		c.committee = envInt("N42_BLS_COMMITTEE", 512)
		c.rampBlocks = uint64(envInt("N42_BLS_RAMP_BLOCKS", 1000000))
		_, pks, derr := blspool.DeriveKeys(seed, c.poolSize, false)
		if derr != nil {
			c.poolErr = derr
			return
		}
		c.poolPks = pks
	})
	return c.poolErr
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
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
