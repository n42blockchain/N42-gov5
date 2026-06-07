// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel/handlers (import paths rewritten).

//go:build n42el

package handlers

import (
	"github.com/libp2p/go-libp2p/core/network"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/sentinel/communication/ssz_snappy"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	MaxRequestsBlocks = 96
)

func (c *ConsensusHandlers) beaconBlocksByRangeHandler(s network.Stream) error {

	req := &cltypes.BeaconBlocksByRangeRequest{}
	if err := ssz_snappy.DecodeAndReadNoForkDigest(s, req, clparams.Phase0Version); err != nil {
		return err
	}

	// Consume additional rate-limit tokens proportional to the requested block count.
	if cost := min(int(req.Count), MaxRequestsBlocks) - 1; !c.consumeRateLimit(s, cost) {
		return nil
	}

	tx, err := c.indiciesDB.BeginRo(c.ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	written := uint64(0)
	for slot := req.StartSlot; slot < req.StartSlot+MaxRequestsBlocks; slot++ {
		block, err := c.beaconDB.ReadBlockBySlot(c.ctx, tx, slot)
		if err != nil {
			return err
		}
		if block == nil {
			continue
		}

		// Read the fork digest
		forkDigest, err := c.ethClock.ComputeForkDigest(slot / c.beaconConfig.SlotsPerEpoch)
		if err != nil {
			return err
		}

		if _, err := s.Write([]byte{0}); err != nil {
			return err
		}
		if _, err := s.Write(forkDigest[:]); err != nil {
			return err
		}
		// obtain block and write
		if err := ssz_snappy.EncodeAndWrite(s, block); err != nil {
			return err
		}
		written++
		if written >= req.Count {
			break
		}
	}

	return nil
}

func (c *ConsensusHandlers) beaconBlocksByRootHandler(s network.Stream) error {

	var req solid.HashListSSZ = solid.NewHashList(100)
	if err := ssz_snappy.DecodeAndReadNoForkDigest(s, req, clparams.Phase0Version); err != nil {
		return err
	}

	blockRoots := []common.Hash{}
	for i := 0; i < req.Length(); i++ {
		blockRoot := req.Get(i)
		blockRoots = append(blockRoots, blockRoot)
		if len(blockRoots) >= MaxRequestsBlocks {
			break
		}
	}

	if len(blockRoots) == 0 {
		return ssz_snappy.EncodeAndWrite(s, &emptyString{}, ResourceUnavailablePrefix)
	}

	if cost := len(blockRoots) - 1; !c.consumeRateLimit(s, cost) {
		return nil
	}

	tx, err := c.indiciesDB.BeginRo(c.ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var block *cltypes.SignedBeaconBlock
	req.Range(func(index int, blockRoot common.Hash, length int) bool {
		block, err = c.beaconDB.ReadBlockByRoot(c.ctx, tx, blockRoot)
		if err != nil {
			return false
		}
		// Recently received blocks (e.g. via gossip) may not have been persisted yet.
		if block == nil && c.forkChoiceReader != nil {
			block, _ = c.forkChoiceReader.GetBlock(blockRoot)
		}
		if block == nil {
			log.Debug("[Sentinel] beaconBlocksByRoot: block not found", "root", blockRoot)
			return true
		}

		forkDigest, err := c.ethClock.ComputeForkDigest(block.Block.Slot / c.beaconConfig.SlotsPerEpoch)
		if err != nil {
			return false
		}

		if _, err = s.Write([]byte{0}); err != nil {
			return false
		}

		if _, err = s.Write(forkDigest[:]); err != nil {
			return false
		}
		if err = ssz_snappy.EncodeAndWrite(s, block); err != nil {
			return false
		}
		return true
	})

	return err
}

type emptyString struct{}

func (e *emptyString) EncodeSSZ(xs []byte) ([]byte, error) {
	return append(xs, 0), nil
}

func (e *emptyString) EncodingSizeSSZ() int {
	return 1
}
