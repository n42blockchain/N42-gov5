// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ported from erigon cl/sentinel/handlers (import paths rewritten).

//go:build n42el

package handlers

import (
	"encoding/hex"
	"io"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/sentinel/communication/ssz_snappy"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// Type safe handlers which all have access to the original stream & decompressed data.

func (c *ConsensusHandlers) pingHandler(s network.Stream) error {
	return ssz_snappy.EncodeAndWrite(s, &cltypes.Ping{
		Id: c.me.Seq(),
	}, SuccessfulResponsePrefix)
}

func (c *ConsensusHandlers) goodbyeHandler(s network.Stream) error {
	peerId := s.Conn().RemotePeer().String()
	gid := &cltypes.Ping{}
	if s.Conn().IsClosed() {
		return nil
	}
	if err := ssz_snappy.DecodeAndReadNoForkDigest(s, gid, clparams.Phase0Version); err != nil {
		if strings.Contains(err.Error(), "stream reset") {
			return nil
		}
		return err
	}

	if gid.Id > 250 { // 250 is the status code for getting banned due to whatever reason
		v, err := c.host.Peerstore().Get("AgentVersion", peerId)
		if err == nil {
			log.Warn("Received goodbye message from peer", "v", v)
		}
	}
	// ignore the error from goodbye, we don't care about it
	ssz_snappy.EncodeAndWrite(s, &cltypes.Ping{
		Id: 1,
	}, SuccessfulResponsePrefix)
	return nil
}

func (c *ConsensusHandlers) metadataV1Handler(s network.Stream) error {
	subnetField := [8]byte{}
	attSubEnr := enr.WithEntry(c.netCfg.AttSubnetKey, &subnetField)
	if err := c.me.Node().Load(attSubEnr); err != nil {
		return err
	}
	return ssz_snappy.EncodeAndWrite(s, &cltypes.Metadata{
		SeqNumber: c.me.Seq(),
		Attnets:   subnetField,
	}, SuccessfulResponsePrefix)
}

func (c *ConsensusHandlers) metadataV2Handler(s network.Stream) error {
	subnetField := [8]byte{}
	syncnetField := [1]byte{}
	attSubEnr := enr.WithEntry(c.netCfg.AttSubnetKey, &subnetField)
	syncNetEnr := enr.WithEntry(c.netCfg.SyncCommsSubnetKey, &syncnetField)
	if err := c.me.Node().Load(attSubEnr); err != nil {
		return err
	}
	if err := c.me.Node().Load(syncNetEnr); err != nil {
		return err
	}

	return ssz_snappy.EncodeAndWrite(s, &cltypes.Metadata{
		SeqNumber: c.me.Seq(),
		Attnets:   subnetField,
		Syncnets:  &syncnetField,
	}, SuccessfulResponsePrefix)
}

func (c *ConsensusHandlers) metadataV3Handler(s network.Stream) error {
	if c.ethClock.GetCurrentEpoch() < c.beaconConfig.FuluForkEpoch {
		return nil
	}
	subnetField := [8]byte{}
	syncnetField := [1]byte{}
	attSubEnr := enr.WithEntry(c.netCfg.AttSubnetKey, &subnetField)
	syncNetEnr := enr.WithEntry(c.netCfg.SyncCommsSubnetKey, &syncnetField)
	if err := c.me.Node().Load(attSubEnr); err != nil {
		return err
	}
	if err := c.me.Node().Load(syncNetEnr); err != nil {
		return err
	}

	cgc := c.peerdasStateReader.GetAdvertisedCgc()
	return ssz_snappy.EncodeAndWrite(s, &cltypes.Metadata{
		SeqNumber:         c.me.Seq(),
		Attnets:           subnetField,
		Syncnets:          &syncnetField,
		CustodyGroupCount: &cgc,
	}, SuccessfulResponsePrefix)
}

func (c *ConsensusHandlers) statusHandler(s network.Stream) error {
	// Per eth2 spec the responder must read the peer's Status before replying.
	peerStatus := &cltypes.Status{}
	if err := ssz_snappy.DecodeAndReadNoForkDigest(s, peerStatus, clparams.Phase0Version); err != nil {
		_, _ = io.Copy(io.Discard, s)
	}
	status := c.hs.Status()
	status.EarliestAvailableSlot = nil
	return ssz_snappy.EncodeAndWrite(s, status, SuccessfulResponsePrefix)
}

func (c *ConsensusHandlers) statusV2Handler(s network.Stream) error {
	// Per eth2 spec the responder must read the peer's Status before replying.
	peerStatus := &cltypes.Status{}
	if err := ssz_snappy.DecodeAndReadNoForkDigest(s, peerStatus, clparams.Phase0Version); err != nil {
		_, _ = io.Copy(io.Discard, s)
	}
	status := c.hs.Status()
	forkDigest, err := c.ethClock.CurrentForkDigest()
	if err != nil {
		return err
	}
	copy(status.ForkDigest[:], forkDigest[:])
	// StatusV2 requires EarliestAvailableSlot (92 bytes total).
	if status.EarliestAvailableSlot == nil {
		eas := c.peerdasStateReader.GetEarliestAvailableSlot()
		status.EarliestAvailableSlot = &eas
	}
	log.Trace("statusV2Handler", "forkDigest", hex.EncodeToString(status.ForkDigest[:]), "finalizedRoot", hex.EncodeToString(status.FinalizedRoot[:]),
		"finalizedEpoch", status.FinalizedEpoch, "headSlot", status.HeadSlot, "headRoot", hex.EncodeToString(status.HeadRoot[:]), "earliestAvailableSlot", *status.EarliestAvailableSlot)
	return ssz_snappy.EncodeAndWrite(s, status, SuccessfulResponsePrefix)
}
