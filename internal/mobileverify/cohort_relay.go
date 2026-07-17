// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// P2P wiring for CohortCoordinator's two cross-node announcements — modeled
// directly on RegistrationService (replication.go): a thin encode/decode
// pair plus a subscribe loop per topic, no new P2P machinery. Both payloads
// are small and self-delimiting (a fixed-size header followed by a sparse
// mask that runs to the end of the message), matching the existing
// mobileverify wire-format conventions (registration is pubkey||pop, no
// length prefixes needed).

package mobileverify

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

const (
	// indexAnnHeaderLen = blockHash(32) || blockNumber(8 BE) || reporter(20).
	indexAnnHeaderLen = 32 + 8 + 20
	// certAnnHeaderLen = reporter(20) || blockHash(32) || blockNumber(8 BE)
	// || receiptsRoot(32) || aggregateSig(96) || windowClosedAt(8 BE).
	certAnnHeaderLen = 20 + 32 + 8 + 32 + 96 + 8
)

func encodeIndexAnnouncement(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment) ([]byte, error) {
	body, err := encodeIndexCommitments(indices)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, indexAnnHeaderLen+len(body))
	out = append(out, blockHash[:]...)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], blockNumber)
	out = append(out, nb[:]...)
	out = append(out, reporter[:]...)
	out = append(out, body...)
	return out, nil
}

func decodeIndexAnnouncement(b []byte, registryBound int) (blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment, err error) {
	if len(b) < indexAnnHeaderLen {
		return blockHash, 0, reporter, nil, fmt.Errorf("mobileverify: index announcement too short (%d bytes)", len(b))
	}
	copy(blockHash[:], b[:32])
	blockNumber = binary.BigEndian.Uint64(b[32:40])
	copy(reporter[:], b[40:60])
	indices, err = decodeIndexCommitments(b[60:], registryBound)
	return blockHash, blockNumber, reporter, indices, err
}

func encodeCertAnnouncement(reporter types.Address, cert *MobileAttestationCert) []byte {
	out := make([]byte, 0, certAnnHeaderLen+len(cert.SignerMask))
	out = append(out, reporter[:]...)
	out = append(out, cert.BlockHash[:]...)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], cert.BlockNumber)
	out = append(out, nb[:]...)
	out = append(out, cert.ReceiptsRoot[:]...)
	out = append(out, cert.AggregateSig[:]...)
	var tb [8]byte
	binary.BigEndian.PutUint64(tb[:], cert.WindowClosedAt)
	out = append(out, tb[:]...)
	out = append(out, cert.SignerMask...)
	return out
}

func decodeCertAnnouncement(b []byte, registryBound int) (reporter types.Address, cert *MobileAttestationCert, err error) {
	if len(b) < certAnnHeaderLen {
		return reporter, nil, fmt.Errorf("mobileverify: cert announcement too short (%d bytes)", len(b))
	}
	copy(reporter[:], b[:20])
	c := &MobileAttestationCert{}
	copy(c.BlockHash[:], b[20:52])
	c.BlockNumber = binary.BigEndian.Uint64(b[52:60])
	copy(c.ReceiptsRoot[:], b[60:92])
	copy(c.AggregateSig[:], b[92:188])
	c.WindowClosedAt = binary.BigEndian.Uint64(b[188:196])
	c.SignerMask = append([]byte(nil), b[196:]...)
	// Validate the mask framing here. CohortCoordinator.OnPeerCert performs the
	// load-bearing aggregate-signature verification against the registry before
	// admitting the certificate to a merge bucket.
	if _, derr := DecodeMask(c.SignerMask, registryBound); derr != nil {
		return reporter, nil, fmt.Errorf("mobileverify: cert announcement mask: %w", derr)
	}
	return reporter, c, nil
}

// CohortRelay wires CohortCoordinator's outbound index/cert announcements to
// two gossip topics and feeds inbound announcements back into it. Optional:
// a coordinator with no relay attached still works correctly in single-node
// mode (nothing to merge with, every window trivially finalizes on its own
// local cert — see coordinator_test.go).
type CohortRelay struct {
	coord      *CohortCoordinator
	p2p        PacketPublisher
	reg        *Registry
	indexTopic string
	certTopic  string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewCohortRelay wires coord's outbound sinks to publish on indexTopic /
// certTopic, and returns the relay so Start can begin the two receive loops.
// Registering the outbound sinks happens immediately (construction time),
// independent of Start/Stop, matching RegistrationService's SetOnRegister
// pattern — a coordinator with a relay always announces, even before the
// receive loops are subscribed.
func NewCohortRelay(coord *CohortCoordinator, p2p PacketPublisher, reg *Registry, indexTopic, certTopic string) *CohortRelay {
	ctx, cancel := context.WithCancel(context.Background())
	r := &CohortRelay{coord: coord, p2p: p2p, reg: reg, indexTopic: indexTopic, certTopic: certTopic, ctx: ctx, cancel: cancel}
	coord.SetIndexAnnounceSink(func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment) {
		if r.p2p == nil {
			return
		}
		payload, err := encodeIndexAnnouncement(blockHash, blockNumber, reporter, indices)
		if err != nil {
			log.Debug("mobileverify: encode index announcement failed", "err", err)
			return
		}
		if err := r.p2p.PublishToTopic(r.ctx, r.indexTopic, payload); err != nil {
			log.Debug("mobileverify: index announcement publish failed", "err", err)
		}
	})
	coord.SetCertAnnounceSink(func(reporter types.Address, cert *MobileAttestationCert) {
		if r.p2p == nil {
			return
		}
		if err := r.p2p.PublishToTopic(r.ctx, r.certTopic, encodeCertAnnouncement(reporter, cert)); err != nil {
			log.Debug("mobileverify: cert announcement publish failed", "err", err)
		}
	})
	return r
}

// Start subscribes both receive loops. No-op (returns nil) if p2p is nil —
// mirrors PacketService/RegistrationService's convention of tolerating a
// disabled P2P layer in single-node/test configurations.
func (r *CohortRelay) Start() error {
	if r.p2p == nil {
		return nil
	}
	indexSub, err := r.p2p.SubscribeToTopic(r.indexTopic)
	if err != nil {
		return fmt.Errorf("mobileverify: subscribe cohort index topic: %w", err)
	}
	certSub, err := r.p2p.SubscribeToTopic(r.certTopic)
	if err != nil {
		indexSub.Cancel()
		return fmt.Errorf("mobileverify: subscribe cohort cert topic: %w", err)
	}
	r.wg.Add(2)
	go r.indexReceiveLoop(indexSub)
	go r.certReceiveLoop(certSub)
	log.Info("mobileverify: cohort relay started", "indexTopic", r.indexTopic, "certTopic", r.certTopic)
	return nil
}

// Stop ends both receive loops.
func (r *CohortRelay) Stop() {
	r.cancel()
	r.wg.Wait()
}

func (r *CohortRelay) indexReceiveLoop(sub *pubsub.Subscription) {
	defer r.wg.Done()
	defer sub.Cancel()
	for {
		msg, err := sub.Next(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			continue
		}
		blockHash, _, reporter, indices, derr := decodeIndexAnnouncement(msg.Data, r.reg.IndexBound())
		if derr != nil {
			log.Debug("mobileverify: rejected malformed cohort index announcement", "err", derr)
			continue
		}
		r.coord.OnPeerIndexSet(blockHash, reporter, indices)
	}
}

func (r *CohortRelay) certReceiveLoop(sub *pubsub.Subscription) {
	defer r.wg.Done()
	defer sub.Cancel()
	for {
		msg, err := sub.Next(r.ctx)
		if err != nil {
			if r.ctx.Err() != nil {
				return
			}
			continue
		}
		reporter, cert, derr := decodeCertAnnouncement(msg.Data, r.reg.IndexBound())
		if derr != nil {
			log.Debug("mobileverify: rejected malformed cohort cert announcement", "err", derr)
			continue
		}
		r.coord.OnPeerCert(reporter, cert)
	}
}
