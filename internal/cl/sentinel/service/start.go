// Copyright 2024-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel/service. Adaptation (#34): embedded-only — the
// sentinel is consumed in-process via direct.NewSentinelClientDirect, so the
// gRPC server (StartServe / RegisterSentinelServer) is dropped along with its
// grpc/credentials deps. ServerConfig keeps only InitialStatus.

//go:build n42el

package service

import (
	"context"

	"github.com/n42blockchain/N42/internal/cl/cltypes"
	peerdasstate "github.com/n42blockchain/N42/internal/cl/das/state"
	"github.com/n42blockchain/N42/internal/cl/p2p"
	"github.com/n42blockchain/N42/internal/cl/persistence/blob_storage"
	"github.com/n42blockchain/N42/internal/cl/phase1/forkchoice"
	"github.com/n42blockchain/N42/internal/cl/sentinel"
	"github.com/n42blockchain/N42/internal/cl/utils/eth_clock"
	"github.com/n42blockchain/N42/internal/cl/depshim/direct"
	"github.com/n42blockchain/N42/internal/cl/depshim/sentinelproto"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/internal/cl/depshim/freezeblocks"
)

const AttestationSubnetSubscriptions = 2

type ServerConfig struct {
	Network       string
	Addr          string
	InitialStatus *cltypes.Status
}

func createSentinel(
	ctx context.Context,
	cfg *sentinel.SentinelConfig,
	blockReader freezeblocks.BeaconSnapshotReader,
	blobStorage blob_storage.BlobStorage,
	indiciesDB kv.RwDB,
	forkChoiceReader forkchoice.ForkChoiceStorageReader,
	ethClock eth_clock.EthereumClock,
	dataColumnStorage blob_storage.DataColumnStorage,
	peerDasStateReader peerdasstate.PeerDasStateReader,
	p2p p2p.P2PManager,
	initialStatus *cltypes.Status,
	logger log.Logger) (*sentinel.Sentinel, *enode.LocalNode, error) {
	sent, err := sentinel.New(
		ctx,
		cfg,
		ethClock,
		blockReader,
		blobStorage,
		indiciesDB,
		logger,
		forkChoiceReader,
		dataColumnStorage,
		peerDasStateReader,
		p2p,
	)
	if err != nil {
		return nil, nil, err
	}
	// Set initial status BEFORE starting the listener so peers connecting
	// immediately see a valid Status instead of all-zeros.
	if initialStatus != nil {
		sent.SetStatus(initialStatus)
	}
	localNode, err := sent.Start()
	if err != nil {
		return nil, nil, err
	}

	// Tie the libp2p host lifetime to the caller's context: libp2p.New does not
	// bind the host to a context, so without this the host (TCP listener + peer
	// conns) would leak when the cl.Service stops. The sentinel's other
	// goroutines (gossip manager, consensus handlers, rate-limiter cleanup,
	// listenForPeers) already observe ctx via sentinel.New above.
	go func() {
		<-ctx.Done()
		sent.Stop()
	}()

	return sent, localNode, nil
}

// StartSentinelService builds the sentinel and returns an in-process
// SentinelClient (direct, no gRPC hop) for cl/rpc.BeaconRpcP2P to consume.
func StartSentinelService(
	ctx context.Context,
	cfg *sentinel.SentinelConfig,
	blockReader freezeblocks.BeaconSnapshotReader,
	blobStorage blob_storage.BlobStorage,
	indiciesDB kv.RwDB,
	srvCfg *ServerConfig,
	ethClock eth_clock.EthereumClock,
	forkChoiceReader forkchoice.ForkChoiceStorageReader,
	dataColumnStorage blob_storage.DataColumnStorage,
	PeerDasStateReader peerdasstate.PeerDasStateReader,
	p2p p2p.P2PManager,
	logger log.Logger) (sentinelproto.SentinelClient, *enode.LocalNode, error) {
	sent, localNode, err := createSentinel(
		ctx,
		cfg,
		blockReader,
		blobStorage,
		indiciesDB,
		forkChoiceReader,
		ethClock,
		dataColumnStorage,
		PeerDasStateReader,
		p2p,
		srvCfg.InitialStatus,
		logger,
	)
	if err != nil {
		return nil, nil, err
	}
	logger.Info("[Sentinel] Sentinel started", "enr", sent.String())
	server := NewSentinelServer(ctx, sent, logger)

	return direct.NewSentinelClientDirect(server), localNode, nil
}
