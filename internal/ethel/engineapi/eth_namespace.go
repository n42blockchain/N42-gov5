// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Minimal eth + net + web3 namespace handlers for the engineAPI
// listener. Lighthouse + Prysm + any standards-compliant CL upcheck
// the EL by calling eth_syncing / eth_chainId / eth_blockNumber
// before they're willing to push newPayload — without these the CL
// logs "Error during execution engine upcheck" forever and never
// forwards any payload, so the wire is unusable.
//
// We don't expose the full eth namespace (account state, logs,
// receipts, etc.); the goal is just enough surface to satisfy the
// EL-CL contract for execution-layer driving.

package engineapi

import (
	"context"
	"encoding/binary"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// EthAPIMinimal implements just enough of the standard eth_* JSON-RPC
// surface for a CL upcheck to pass and the standard sync-status
// reporting flow to work.
type EthAPIMinimal struct {
	db       kv.RoDB
	chainCfg *params.ChainConfig
}

// NewEthAPIMinimal constructs a stateless minimal handler.
func NewEthAPIMinimal(db kv.RoDB, cfg *params.ChainConfig) *EthAPIMinimal {
	return &EthAPIMinimal{db: db, chainCfg: cfg}
}

// Syncing returns false when the EL has nothing to sync. The CL uses
// this to decide whether to push payloads. eth-el has no internal sync
// loop — every block it learns about comes from the CL via newPayload
// — so from the CL's perspective the EL is never "syncing on its own"
// and we always return false.
func (e *EthAPIMinimal) Syncing(ctx context.Context) (interface{}, error) {
	return false, nil
}

// ChainId reports the chain id (mainnet = 1). CLs that route blob
// validation by chain id need this.
func (e *EthAPIMinimal) ChainId() *hexutil.Big {
	if e.chainCfg == nil || e.chainCfg.ChainID == nil {
		v := hexutil.Big{}
		return &v
	}
	v := hexutil.Big(*e.chainCfg.ChainID)
	return &v
}

// BlockNumber returns the canonical head block number. Reads
// directly from chaindata so the value reflects whatever the
// CatchupService + EngineStateAdapter have committed.
func (e *EthAPIMinimal) BlockNumber(ctx context.Context) (hexutil.Uint64, error) {
	if e.db == nil {
		return 0, nil
	}
	tx, err := e.db.BeginRo(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	headHash := rawdb.ReadHeadBlockHash(tx)
	if headHash == (types.Hash{}) {
		// eldevp2p (snapshot-direct / hashed-canonical) never writes that
		// record — it tracks the head in the progress marker. Without this a
		// fully caught-up node answers 0.
		if n, ok := ethel.ReadHeadMarker(tx); ok {
			return hexutil.Uint64(n), nil
		}
		return 0, nil
	}
	num := rawdb.ReadHeaderNumber(tx, headHash)
	if num == nil {
		if n, ok := ethel.ReadHeadMarker(tx); ok {
			return hexutil.Uint64(n), nil
		}
		return 0, nil
	}
	return hexutil.Uint64(*num), nil
}

// NetAPIMinimal implements the net_version method some CLs call as
// part of their EL upcheck.
type NetAPIMinimal struct{ chainCfg *params.ChainConfig }

func NewNetAPIMinimal(cfg *params.ChainConfig) *NetAPIMinimal {
	return &NetAPIMinimal{chainCfg: cfg}
}

// Version returns the network id as a decimal string ("1" for mainnet).
func (n *NetAPIMinimal) Version() string {
	if n.chainCfg == nil || n.chainCfg.ChainID == nil {
		return "0"
	}
	return n.chainCfg.ChainID.String()
}

// Listening reports whether the EL is reachable over P2P. eth-el
// runs its own libp2p stack but the relevant fact for the CL is
// just "is the EL up", which it is if we're answering this RPC.
func (n *NetAPIMinimal) Listening() bool { return true }

// PeerCount returns 0 since eth-el doesn't currently expose its
// libp2p peer count through engineAPI. CLs treat 0 as "EL has no
// blockchain peers" — fine because all chain data flows via the
// engine API anyway.
func (n *NetAPIMinimal) PeerCount() hexutil.Uint { return 0 }

// Web3APIMinimal implements web3_clientVersion which some CLs log.
type Web3APIMinimal struct{}

func NewWeb3APIMinimal() *Web3APIMinimal { return &Web3APIMinimal{} }

// ClientVersion returns a human-readable identifier for the EL.
func (Web3APIMinimal) ClientVersion() string {
	return "n42-eth-el/v0"
}

// uint64FromBytes is a helper used in some readers; kept here so the
// package has no unused imports if the encoding/binary import drops
// out of the active method bodies above.
func uint64FromBytes(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}
