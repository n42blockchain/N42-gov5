// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// /rpc/commit_qc/1/ssz_snappy (N42 sync standard §5.2): the anchor for an
// unanchored range tail, for observers that receive no Decide, and for
// state-sync pivots. The payload is the commit_qc/1 layout the offline
// snapshot bundle already uses (snapshotjoin.EncodeAnchor):
//
//	qc_len u32 ‖ QC wire ‖ header_count u16 ‖ header_count × (u32 len ‖ RLP header)

package sync

import (
	"context"
	"errors"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
)

func (s *Service) commitQCHandler(ctx context.Context, pid peer.ID, raw []byte, stream network.Stream) {
	req, err := syncwire.DecodeCommitQCRequest(raw)
	if err != nil {
		writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error()))
		return
	}
	var (
		payload []byte
		code    byte
		msg     string
	)
	head := currentBlockNumberOrZero(s.cfg.chain)
	if ceil, ok := s.committedServeCeiling(); ok {
		head = ceil
	}
	if err := s.cfg.chain.DB().View(ctx, func(tx kv.Tx) error {
		payload, code, msg = v11.ServeCommitQC(v11.TxCommitQCSource{Tx: tx, Head: head}, req)
		return nil
	}); err != nil {
		code, msg = syncwire.CodeServerError, "database"
	}
	if code != syncwire.CodeSuccess {
		writeV11(stream, syncwire.AppendError(nil, code, msg))
		return
	}
	writeV11(stream, syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, payload))
	log.Debug("commit_qc: served", "peer", shortPeer(pid), "number", req.Number, "bytes", len(payload))
}

// RequestCommitQC asks pid for the commit QC anchoring (number, hash).
func RequestCommitQC(ctx context.Context, h host.Host, pid peer.ID, req syncwire.CommitQCRequest) (*v11.CommitQCReply, error) {
	stream, err := openV11(ctx, h, pid, syncwire.ProtocolCommitQC, req.Encode())
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	return v11.ReadCommitQC(stream, req)
}

// VerifyCommitQCWith verifies a reply's QC strictly (commit domain, the set
// of its own epoch, no fallback) through the consensus-side header-QC
// verifier.
func VerifyCommitQCWith(v syncanchor.Verifier, qc *hotstuff.QuorumCertificate) syncanchor.Result {
	extra, err := hotstuff.BuildHeaderExtra(qc.View+1, qc)
	if err != nil {
		return syncanchor.Result{Verdict: syncanchor.BadExtra, Err: err}
	}
	return v.VerifyHeaderQC(extra)
}

// anchorTailViaCommitQC asks peers speaking commit_qc/1 for a QC anchoring
// the top of the held tail, and releases the tail it authenticates. It
// returns the number of blocks imported.
func (s *Service) anchorTailViaCommitQC(peers []peer.ID, authorized bool) int {
	v := s.anchorVerifier()
	h := hostOf(s.cfg.p2p)
	if v == nil || h == nil {
		return 0
	}
	s.anchors.mu.Lock()
	if len(s.anchors.tail) == 0 {
		s.anchors.mu.Unlock()
		return 0
	}
	top := s.anchors.tail[len(s.anchors.tail)-1]
	s.anchors.mu.Unlock()
	req := syncwire.CommitQCRequest{Number: top.Number64().Uint64(), Hash: top.Hash()}
	for _, pid := range peers {
		if !peerSpeaks(h, pid, syncwire.ProtocolCommitQC) {
			continue
		}
		ctx, cancel := context.WithTimeout(s.ctx, ttfbTimeout)
		rep, err := RequestCommitQC(ctx, h, pid, req)
		cancel()
		if err != nil {
			if errors.Is(err, v11.ErrCommitQCFault) {
				s.noteCatchUpInvalid(pid)
			}
			log.Debug("commit_qc: request failed", "peer", shortPeer(pid), "number", req.Number, "err", err)
			continue
		}
		if len(rep.Headers) == 0 {
			continue // the anchor is further than 256 headers away
		}
		r := VerifyCommitQCWith(v, rep.QC)
		switch r.Verdict {
		case syncanchor.Verified:
		case syncanchor.BadQC:
			s.noteCatchUpInvalid(pid)
			continue
		default:
			continue
		}
		s.anchors.recordHeaderLinks(rep.Headers)
		s.anchors.noteAnchor(rep.QC.BlockHash)
		log.Info("commit_qc: anchored the held tail", "peer", shortPeer(pid), "tailTop", req.Number,
			"qcBlock", rep.Headers[0].Number.Uint64(), "qcView", rep.QC.View)
		return s.releaseAnchoredTail(authorized)
	}
	return 0
}
