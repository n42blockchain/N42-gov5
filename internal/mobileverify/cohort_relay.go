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
	"sort"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

const (
	// indexAnnHeaderLen = blockHash(32) || blockNumber(8 BE) || reporter(20)
	// || reporterSig(96). The reporter signature sits before the variable
	// (index,commitment) body, which runs to the end of the message.
	indexAnnHeaderLen = 32 + 8 + 20 + CohortSigLen
	// certAnnHeaderLen = reporter(20) || reporterSig(96) || blockHash(32) ||
	// blockNumber(8 BE) || receiptsRoot(32) || aggregateSig(96) ||
	// windowClosedAt(8 BE), followed by the variable signer mask.
	certAnnHeaderLen = 20 + CohortSigLen + 32 + 8 + 32 + 96 + 8
	// revealAnnHeaderLen = blockHash(32) || blockNumber(8 BE) || reporter(20)
	// || reporterSig(96), followed by the variable reveal body (count + per
	// entry index||sig(96)||root(32)).
	revealAnnHeaderLen = 32 + 8 + 20 + CohortSigLen
	// revealEntryLen = sig(96) || root(32), the fixed part after each entry's
	// uvarint index.
	revealEntryLen = CohortSigLen + 32
)

// encodeIndexAnnouncement signs the (index,commitment) body with sign (the
// reporter's validator BLS key) and lays it out on the wire. sign==nil (or a
// wrong-length signature) writes a zero signature — accepted only by a relay
// whose verifier is also nil (single-node/test).
func encodeIndexAnnouncement(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment, sign CohortSigner) ([]byte, error) {
	body, err := encodeIndexCommitments(indices)
	if err != nil {
		return nil, err
	}
	sig := signOrZero(sign, cohortIndexAuthMessage(blockHash, blockNumber, reporter, body))
	out := make([]byte, 0, indexAnnHeaderLen+len(body))
	out = append(out, blockHash[:]...)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], blockNumber)
	out = append(out, nb[:]...)
	out = append(out, reporter[:]...)
	out = append(out, sig...)
	out = append(out, body...)
	return out, nil
}

// decodeIndexAnnouncement parses and, when verify!=nil, authenticates the
// announcement: reporter must be an authorized validator and the signature
// must verify over the body. An unauthenticated announcement is rejected.
func decodeIndexAnnouncement(b []byte, registryBound int, verify CohortVerifier) (blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment, err error) {
	if len(b) < indexAnnHeaderLen {
		return blockHash, 0, reporter, nil, fmt.Errorf("mobileverify: index announcement too short (%d bytes)", len(b))
	}
	copy(blockHash[:], b[:32])
	blockNumber = binary.BigEndian.Uint64(b[32:40])
	copy(reporter[:], b[40:60])
	sig := b[60 : 60+CohortSigLen]
	body := b[60+CohortSigLen:]
	if verify != nil && !verify(reporter, cohortIndexAuthMessage(blockHash, blockNumber, reporter, body), sig) {
		return blockHash, blockNumber, reporter, nil, fmt.Errorf("mobileverify: unauthenticated index announcement from %s", reporter)
	}
	indices, err = decodeIndexCommitments(body, registryBound)
	return blockHash, blockNumber, reporter, indices, err
}

// encodeRevealAnnouncement lays out a Layer 2B reveal on the wire, signing the
// reveal body with the reporter's validator BLS key. The body is deterministic
// (entries sorted by index) so the signature is reproducible.
func encodeRevealAnnouncement(blockHash types.Hash, blockNumber uint64, reporter types.Address, reveal map[MobileIndex]RevealedSig, sign CohortSigner) []byte {
	body := encodeRevealBody(reveal)
	sig := signOrZero(sign, cohortAuthMessage(cohortRevealAuthDomain, blockHash, blockNumber, reporter, body))
	out := make([]byte, 0, revealAnnHeaderLen+len(body))
	out = append(out, blockHash[:]...)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], blockNumber)
	out = append(out, nb[:]...)
	out = append(out, reporter[:]...)
	out = append(out, sig...)
	out = append(out, body...)
	return out
}

// decodeRevealAnnouncement parses and, when verify!=nil, authenticates a reveal
// announcement. registryBound caps the entry count so a malformed message
// cannot force an unbounded allocation.
func decodeRevealAnnouncement(b []byte, registryBound int, verify CohortVerifier) (blockHash types.Hash, blockNumber uint64, reporter types.Address, reveal map[MobileIndex]RevealedSig, err error) {
	if len(b) < revealAnnHeaderLen {
		return blockHash, 0, reporter, nil, fmt.Errorf("mobileverify: reveal announcement too short (%d bytes)", len(b))
	}
	copy(blockHash[:], b[:32])
	blockNumber = binary.BigEndian.Uint64(b[32:40])
	copy(reporter[:], b[40:60])
	sig := b[60 : 60+CohortSigLen]
	body := b[60+CohortSigLen:]
	if verify != nil && !verify(reporter, cohortAuthMessage(cohortRevealAuthDomain, blockHash, blockNumber, reporter, body), sig) {
		return blockHash, blockNumber, reporter, nil, fmt.Errorf("mobileverify: unauthenticated reveal announcement from %s", reporter)
	}
	reveal, err = decodeRevealBody(body, registryBound)
	return blockHash, blockNumber, reporter, reveal, err
}

// encodeRevealBody serializes the reveal map deterministically: uvarint count,
// then per entry uvarint index || sig(96) || root(32), ascending by index.
func encodeRevealBody(reveal map[MobileIndex]RevealedSig) []byte {
	idxs := make([]MobileIndex, 0, len(reveal))
	for idx := range reveal {
		idxs = append(idxs, idx)
	}
	sortMobileIndex(idxs)
	var tmp [binary.MaxVarintLen64]byte
	out := make([]byte, 0, binary.MaxVarintLen64+len(idxs)*(binary.MaxVarintLen64+revealEntryLen))
	n := binary.PutUvarint(tmp[:], uint64(len(idxs)))
	out = append(out, tmp[:n]...)
	for _, idx := range idxs {
		n := binary.PutUvarint(tmp[:], uint64(idx))
		out = append(out, tmp[:n]...)
		rs := reveal[idx]
		out = append(out, rs.Sig[:]...)
		out = append(out, rs.Root[:]...)
	}
	return out
}

func decodeRevealBody(b []byte, registryBound int) (map[MobileIndex]RevealedSig, error) {
	count, n := binary.Uvarint(b)
	if n <= 0 {
		return nil, fmt.Errorf("mobileverify: reveal body bad count prefix")
	}
	if registryBound > 0 && count > uint64(registryBound) {
		return nil, fmt.Errorf("mobileverify: reveal body count %d exceeds bound %d", count, registryBound)
	}
	b = b[n:]
	out := make(map[MobileIndex]RevealedSig, count)
	for i := uint64(0); i < count; i++ {
		idxV, m := binary.Uvarint(b)
		if m <= 0 {
			return nil, fmt.Errorf("mobileverify: reveal body bad index at entry %d", i)
		}
		b = b[m:]
		if len(b) < revealEntryLen {
			return nil, fmt.Errorf("mobileverify: reveal body truncated at entry %d", i)
		}
		var rs RevealedSig
		copy(rs.Sig[:], b[:CohortSigLen])
		copy(rs.Root[:], b[CohortSigLen:revealEntryLen])
		b = b[revealEntryLen:]
		out[MobileIndex(idxV)] = rs
	}
	return out, nil
}

func sortMobileIndex(idxs []MobileIndex) {
	sort.Slice(idxs, func(i, j int) bool { return idxs[i] < idxs[j] })
}

func encodeCertAnnouncement(reporter types.Address, cert *MobileAttestationCert, sign CohortSigner) []byte {
	body := certAuthBody(cert)
	sig := signOrZero(sign, cohortCertAuthMessage(cert.BlockHash, cert.BlockNumber, reporter, body))
	out := make([]byte, 0, certAnnHeaderLen+len(cert.SignerMask))
	out = append(out, reporter[:]...)
	out = append(out, sig...)
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

func decodeCertAnnouncement(b []byte, registryBound int, verify CohortVerifier) (reporter types.Address, cert *MobileAttestationCert, err error) {
	if len(b) < certAnnHeaderLen {
		return reporter, nil, fmt.Errorf("mobileverify: cert announcement too short (%d bytes)", len(b))
	}
	copy(reporter[:], b[:20])
	sig := b[20 : 20+CohortSigLen]
	rest := b[20+CohortSigLen:]
	c := &MobileAttestationCert{}
	copy(c.BlockHash[:], rest[0:32])
	c.BlockNumber = binary.BigEndian.Uint64(rest[32:40])
	copy(c.ReceiptsRoot[:], rest[40:72])
	copy(c.AggregateSig[:], rest[72:168])
	c.WindowClosedAt = binary.BigEndian.Uint64(rest[168:176])
	c.SignerMask = append([]byte(nil), rest[176:]...)
	if verify != nil && !verify(reporter, cohortCertAuthMessage(c.BlockHash, c.BlockNumber, reporter, certAuthBody(c)), sig) {
		return reporter, nil, fmt.Errorf("mobileverify: unauthenticated cert announcement from %s", reporter)
	}
	// Validate the mask framing here. CohortCoordinator.OnPeerCert performs the
	// load-bearing aggregate-signature verification against the registry before
	// admitting the certificate to a merge bucket.
	if _, derr := DecodeMask(c.SignerMask, registryBound); derr != nil {
		return reporter, nil, fmt.Errorf("mobileverify: cert announcement mask: %w", derr)
	}
	return reporter, c, nil
}

// certAuthBody is the canonical byte string a reporter's signature covers for a
// cert announcement — every field it attests to, excluding the reporter and
// signature themselves.
func certAuthBody(c *MobileAttestationCert) []byte {
	body := make([]byte, 0, 32+8+32+96+8+len(c.SignerMask))
	body = append(body, c.BlockHash[:]...)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], c.BlockNumber)
	body = append(body, nb[:]...)
	body = append(body, c.ReceiptsRoot[:]...)
	body = append(body, c.AggregateSig[:]...)
	binary.BigEndian.PutUint64(nb[:], c.WindowClosedAt)
	body = append(body, nb[:]...)
	body = append(body, c.SignerMask...)
	return body
}

// signOrZero returns sign(msg) when it yields a correctly-sized signature,
// otherwise a zero signature (unsigned; only a nil-verifier relay accepts it).
func signOrZero(sign CohortSigner, msg []byte) []byte {
	if sign != nil {
		if s := sign(msg); len(s) == CohortSigLen {
			return s
		}
	}
	return make([]byte, CohortSigLen)
}

// CohortRelay wires CohortCoordinator's outbound index/cert announcements to
// two gossip topics and feeds inbound announcements back into it. Optional:
// a coordinator with no relay attached still works correctly in single-node
// mode (nothing to merge with, every window trivially finalizes on its own
// local cert — see coordinator_test.go).
type CohortRelay struct {
	coord       *CohortCoordinator
	p2p         PacketPublisher
	reg         *Registry
	indexTopic  string
	revealTopic string
	certTopic   string

	// sign authenticates this node's own announcements; verify authenticates
	// inbound ones (reporter ∈ validators && signature valid). Both nil =
	// single-node/test mode with no authentication.
	sign   CohortSigner
	verify CohortVerifier

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
	return NewCohortRelayWithAuth(coord, p2p, reg, indexTopic, "", certTopic, nil, nil)
}

// NewCohortRelayWithAuth is NewCohortRelay plus reporter authentication: sign
// is applied to this node's outbound announcements and verify to inbound ones.
// Pass nil/nil for the unauthenticated single-node/test behaviour. revealTopic
// may be "" to disable the Layer 2B reveal round (index/cert only).
func NewCohortRelayWithAuth(coord *CohortCoordinator, p2p PacketPublisher, reg *Registry, indexTopic, revealTopic, certTopic string, sign CohortSigner, verify CohortVerifier) *CohortRelay {
	ctx, cancel := context.WithCancel(context.Background())
	r := &CohortRelay{coord: coord, p2p: p2p, reg: reg, indexTopic: indexTopic, revealTopic: revealTopic, certTopic: certTopic, sign: sign, verify: verify, ctx: ctx, cancel: cancel}
	coord.SetIndexAnnounceSink(func(blockHash types.Hash, blockNumber uint64, reporter types.Address, indices []IndexCommitment) {
		if r.p2p == nil {
			return
		}
		payload, err := encodeIndexAnnouncement(blockHash, blockNumber, reporter, indices, r.sign)
		if err != nil {
			log.Debug("mobileverify: encode index announcement failed", "err", err)
			return
		}
		if err := r.p2p.PublishToTopic(r.ctx, r.indexTopic, payload); err != nil {
			log.Debug("mobileverify: index announcement publish failed", "err", err)
		}
	})
	coord.SetRevealAnnounceSink(func(blockHash types.Hash, blockNumber uint64, reporter types.Address, reveal map[MobileIndex]RevealedSig) {
		if r.p2p == nil || r.revealTopic == "" {
			return
		}
		payload := encodeRevealAnnouncement(blockHash, blockNumber, reporter, reveal, r.sign)
		if err := r.p2p.PublishToTopic(r.ctx, r.revealTopic, payload); err != nil {
			log.Debug("mobileverify: reveal announcement publish failed", "err", err)
		}
	})
	coord.SetCertAnnounceSink(func(reporter types.Address, cert *MobileAttestationCert) {
		if r.p2p == nil {
			return
		}
		if err := r.p2p.PublishToTopic(r.ctx, r.certTopic, encodeCertAnnouncement(reporter, cert, r.sign)); err != nil {
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
	var revealSub *pubsub.Subscription
	if r.revealTopic != "" {
		revealSub, err = r.p2p.SubscribeToTopic(r.revealTopic)
		if err != nil {
			indexSub.Cancel()
			certSub.Cancel()
			return fmt.Errorf("mobileverify: subscribe cohort reveal topic: %w", err)
		}
	}
	r.wg.Add(2)
	go r.indexReceiveLoop(indexSub)
	go r.certReceiveLoop(certSub)
	if revealSub != nil {
		r.wg.Add(1)
		go r.revealReceiveLoop(revealSub)
	}
	log.Info("mobileverify: cohort relay started", "indexTopic", r.indexTopic, "revealTopic", r.revealTopic, "certTopic", r.certTopic)
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
		blockHash, _, reporter, indices, derr := decodeIndexAnnouncement(msg.Data, r.reg.IndexBound(), r.verify)
		if derr != nil {
			log.Debug("mobileverify: rejected malformed cohort index announcement", "err", derr)
			continue
		}
		r.coord.OnPeerIndexSet(blockHash, reporter, indices)
	}
}

func (r *CohortRelay) revealReceiveLoop(sub *pubsub.Subscription) {
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
		blockHash, _, reporter, reveal, derr := decodeRevealAnnouncement(msg.Data, r.reg.IndexBound(), r.verify)
		if derr != nil {
			log.Debug("mobileverify: rejected malformed cohort reveal announcement", "err", derr)
			continue
		}
		r.coord.OnPeerReveal(blockHash, reporter, reveal)
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
		reporter, cert, derr := decodeCertAnnouncement(msg.Data, r.reg.IndexBound(), r.verify)
		if derr != nil {
			log.Debug("mobileverify: rejected malformed cohort cert announcement", "err", derr)
			continue
		}
		r.coord.OnPeerCert(reporter, cert)
	}
}
