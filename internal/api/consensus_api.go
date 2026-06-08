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
	"fmt"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// ConsensusAPI exposes per-block consensus evidence (BLS QC + mobile votes).
type ConsensusAPI struct {
	api *API
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
