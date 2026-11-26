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

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
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
	log.Info("commit_qc: served", "peer", shortPeer(pid), "number", req.Number, "bytes", len(payload))
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
	ask := s.commitQCAnchorer()
	if ask == nil {
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
		headers, anchor, ok := ask(pid, req)
		if !ok || len(headers) == 0 {
			continue
		}
		s.anchors.recordHeaderLinks(headers)
		s.anchors.noteAnchor(anchor)
		log.Info("commit_qc: anchored the held tail", "peer", shortPeer(pid), "tailTop", req.Number,
			"qcBlock", headers[0].Number.Uint64())
		return s.releaseAnchoredTail(authorized)
	}
	return 0
}

// commitQCAnchorFn asks one peer for the commit QC anchoring req. ok means
// the QC verified strictly; headers run from the QC's block down to req and
// anchor is the hash the QC names.
type commitQCAnchorFn func(pid peer.ID, req syncwire.CommitQCRequest) (headers []*block.Header, anchor types.Hash, ok bool)

// commitQCAnchorer returns the commit_qc/1 exchange, or nil when this node
// has no QC verifier or no libp2p host.
func (s *Service) commitQCAnchorer() commitQCAnchorFn {
	if s.commitQCAnchorOverride != nil {
		return s.commitQCAnchorOverride
	}
	v := s.anchorVerifier()
	h := hostOf(s.cfg.p2p)
	if v == nil || h == nil {
		return nil
	}
	return func(pid peer.ID, req syncwire.CommitQCRequest) ([]*block.Header, types.Hash, bool) {
		if !peerSpeaks(h, pid, syncwire.ProtocolCommitQC) {
			return nil, types.Hash{}, false
		}
		ctx, cancel := context.WithTimeout(s.ctx, ttfbTimeout)
		rep, err := RequestCommitQC(ctx, h, pid, req)
		cancel()
		if err != nil {
			if errors.Is(err, v11.ErrCommitQCFault) {
				s.noteCatchUpInvalid(pid)
			}
			log.Debug("commit_qc: request failed", "peer", shortPeer(pid), "number", req.Number, "err", err)
			return nil, types.Hash{}, false
		}
		if len(rep.Headers) == 0 {
			return nil, types.Hash{}, false // the anchor is further than 256 headers away
		}
		switch VerifyCommitQCWith(v, rep.QC).Verdict {
		case syncanchor.Verified:
		case syncanchor.BadQC:
			s.noteCatchUpInvalid(pid)
			return nil, types.Hash{}, false
		default:
			return nil, types.Hash{}, false
		}
		log.Debug("commit_qc: verified", "peer", shortPeer(pid), "number", req.Number, "qcView", rep.QC.View)
		return rep.Headers, rep.QC.BlockHash, true
	}
}
