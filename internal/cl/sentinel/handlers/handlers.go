// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel/handlers (import paths rewritten). For the B+
// block-gossip-first transport (#34) the handler registration is trimmed to
// the block + heartbeat req/resp protocols: blocks-by-range/root/head, ping,
// goodbye, status(V1/V2), metadata(V1/V2/V3). The light-client, blob,
// data-column and execution-payload-envelope handlers are intentionally not
// ported (their req/resp protocols are not exercised by block-only fork
// choice). The NewConsensusHandlers signature is kept identical so the sentinel
// wiring is unchanged when those handlers are added later.

//go:build n42el

package handlers

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	peerdasstate "github.com/n42blockchain/N42/internal/cl/das/state"
	"github.com/n42blockchain/N42/internal/cl/persistence/blob_storage"
	"github.com/n42blockchain/N42/internal/cl/phase1/forkchoice"
	"github.com/n42blockchain/N42/internal/cl/sentinel/communication"
	"github.com/n42blockchain/N42/internal/cl/sentinel/communication/ssz_snappy"
	"github.com/n42blockchain/N42/internal/cl/sentinel/handshake"
	"github.com/n42blockchain/N42/internal/cl/sentinel/peers"
	"github.com/n42blockchain/N42/internal/cl/utils/eth_clock"
	"github.com/n42blockchain/N42/internal/cl/depshim/freezeblocks"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

var (
	ErrResourceUnavailable = errors.New("resource unavailable")
)

type ConsensusHandlers struct {
	handlers     map[protocol.ID]network.StreamHandler
	hs           *handshake.HandShaker
	beaconConfig *clparams.BeaconChainConfig
	ethClock     eth_clock.EthereumClock
	ctx          context.Context
	beaconDB     freezeblocks.BeaconSnapshotReader

	indiciesDB         kv.RoDB
	rateLimiter        *peerRateLimiter
	forkChoiceReader   forkchoice.ForkChoiceStorageReader
	host               host.Host
	me                 *enode.LocalNode
	netCfg             *clparams.NetworkConfig
	blobsStorage       blob_storage.BlobStorage
	dataColumnStorage  blob_storage.DataColumnStorage
	peerdasStateReader peerdasstate.PeerDasStateReader
	enableBlocks       bool
}

const (
	SuccessfulResponsePrefix  = 0x00
	InvalidRequestPrefix      = 0x01
	ServerErrorPrefix         = 0x02
	ResourceUnavailablePrefix = 0x03
)

func NewConsensusHandlers(
	ctx context.Context,
	db freezeblocks.BeaconSnapshotReader,
	indiciesDB kv.RoDB,
	host host.Host,
	peers *peers.Pool,
	netCfg *clparams.NetworkConfig,
	me *enode.LocalNode,
	beaconConfig *clparams.BeaconChainConfig,
	ethClock eth_clock.EthereumClock,
	hs *handshake.HandShaker,
	forkChoiceReader forkchoice.ForkChoiceStorageReader,
	blobsStorage blob_storage.BlobStorage,
	dataColumnStorage blob_storage.DataColumnStorage,
	peerDasStateReader peerdasstate.PeerDasStateReader,
	enabledBlocks bool) *ConsensusHandlers {
	c := &ConsensusHandlers{
		host:               host,
		hs:                 hs,
		beaconDB:           db,
		indiciesDB:         indiciesDB,
		ethClock:           ethClock,
		beaconConfig:       beaconConfig,
		ctx:                ctx,
		rateLimiter:        newPeerRateLimiter(),
		enableBlocks:       enabledBlocks,
		forkChoiceReader:   forkChoiceReader,
		me:                 me,
		netCfg:             netCfg,
		blobsStorage:       blobsStorage,
		dataColumnStorage:  dataColumnStorage,
		peerdasStateReader: peerDasStateReader,
	}

	hm := map[string]func(s network.Stream) error{
		communication.PingProtocolV1:     c.pingHandler,
		communication.GoodbyeProtocolV1:  c.goodbyeHandler,
		communication.StatusProtocolV1:   c.statusHandler,
		communication.StatusProtocolV2:   c.statusV2Handler,
		communication.MetadataProtocolV1: c.metadataV1Handler,
		communication.MetadataProtocolV2: c.metadataV2Handler,
		communication.MetadataProtocolV3: c.metadataV3Handler,
	}

	if c.enableBlocks {
		hm[communication.BeaconBlocksByRangeProtocolV2] = c.beaconBlocksByRangeHandler
		hm[communication.BeaconBlocksByRootProtocolV2] = c.beaconBlocksByRootHandler
		// NB: blocks-by-head (consensus-specs PR #5181, Fulu) is intentionally
		// not wired — N42's cltypes/communication predate it, and blocks-by-
		// range/root already cover the B+ block-only sync path.
	}

	c.handlers = map[protocol.ID]network.StreamHandler{}
	for k, v := range hm {
		c.handlers[protocol.ID(k)] = c.wrapStreamHandler(k, v)
	}
	return c
}

func (c *ConsensusHandlers) Start() {
	c.rateLimiter.startCleanup(c.ctx)
	for id, handler := range c.handlers {
		c.host.SetStreamHandler(id, handler)
	}
}

func (c *ConsensusHandlers) wrapStreamHandler(name string, fn func(s network.Stream) error) func(s network.Stream) {
	return func(s network.Stream) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("[pubsubhandler] panic in stream handler", "err", r, "name", name)
				_ = s.Reset()
				_ = s.Close()
			}
		}()

		peerID := s.Conn().RemotePeer().String()

		// Enforce per-peer concurrent stream cap.
		if !c.rateLimiter.acquireConcurrency(peerID) {
			log.Debug("[pubsubhandler] too many concurrent requests", "protocol", name, "peer", peerID)
			_ = ssz_snappy.EncodeAndWrite(s, &emptyString{}, InvalidRequestPrefix)
			_ = s.Close() // graceful close so the peer reads the error response
			return
		}
		defer c.rateLimiter.releaseConcurrency(peerID)

		// Enforce per-peer, per-protocol rate limit.
		if !c.rateLimiter.allowRequest(peerID, name, 1) {
			log.Debug("[pubsubhandler] rate limit exceeded", "protocol", name, "peer", peerID)
			_ = ssz_snappy.EncodeAndWrite(s, &emptyString{}, InvalidRequestPrefix)
			_ = s.Close() // graceful close so the peer reads the error response
			return
		}

		l := log.Ctx{
			"name": name,
		}
		rawVer, err := c.host.Peerstore().Get(s.Conn().RemotePeer(), "AgentVersion")
		if err == nil {
			if str, ok := rawVer.(string); ok {
				l["agent"] = str
			}
		}

		streamDeadline := time.Now().Add(5 * time.Second)
		s.SetReadDeadline(streamDeadline)
		s.SetWriteDeadline(streamDeadline)
		s.SetDeadline(streamDeadline)

		if err := fn(s); err != nil {
			if errors.Is(err, ErrResourceUnavailable) {
				if _, err := s.Write([]byte{ResourceUnavailablePrefix}); err != nil {
					log.Debug("failed to write resource unavailable prefix", "err", err)
				}
			}
			log.Trace("[pubsubhandler] stream handler returned error", "protocol", name, "peer", s.Conn().RemotePeer().String(), "err", err)
			_ = s.Reset()
			_ = s.Close()
			return
		}
		if err := s.Close(); err != nil {
			l["err"] = err
			if !(strings.Contains(name, "goodbye") &&
				(strings.Contains(err.Error(), "session shut down") ||
					strings.Contains(err.Error(), "stream reset"))) {
				log.Trace("[pubsubhandler] close stream", l)
			}
		}
	}
}

// consumeRateLimit consumes additional rate-limit tokens for a batch request.
func (c *ConsensusHandlers) consumeRateLimit(s network.Stream, cost int) bool {
	if cost <= 0 {
		return true
	}
	peerID := s.Conn().RemotePeer().String()
	protocol := string(s.Protocol())
	if !c.rateLimiter.consumeTokens(peerID, protocol, cost) {
		log.Debug("[pubsubhandler] rate limit exceeded (batch)", "protocol", protocol, "peer", peerID, "cost", cost)
		_ = ssz_snappy.EncodeAndWrite(s, &emptyString{}, InvalidRequestPrefix)
		return false
	}
	return true
}
