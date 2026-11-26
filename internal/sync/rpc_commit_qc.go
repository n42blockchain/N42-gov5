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
	"fmt"
	"io"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// commitQCScanLimit bounds how many headers above the target the server
// reads looking for an embedded QC that reaches it.
const commitQCScanLimit = 4096

// commitQCSource is what the commit_qc/1 server reads (one consistent view).
type commitQCSource interface {
	committedHead() uint64
	canonicalHash(n uint64) types.Hash
	headerByHash(h types.Hash) *block.Header
	headerAt(n uint64) *block.Header
	lastCommittedQC() *hotstuff.QuorumCertificate
}

type txCommitQCSource struct {
	tx   kv.Tx
	head uint64
}

func (s txCommitQCSource) committedHead() uint64 { return s.head }
func (s txCommitQCSource) canonicalHash(n uint64) types.Hash {
	h, _ := rawdb.ReadCanonicalHash(s.tx, n)
	return h
}
func (s txCommitQCSource) headerByHash(h types.Hash) *block.Header {
	n := rawdb.ReadHeaderNumber(s.tx, h)
	if n == nil {
		return nil
	}
	return rawdb.ReadHeader(s.tx, h, *n)
}
func (s txCommitQCSource) headerAt(n uint64) *block.Header {
	h := s.canonicalHash(n)
	if h == (types.Hash{}) {
		return nil
	}
	return rawdb.ReadHeader(s.tx, h, n)
}
func (s txCommitQCSource) lastCommittedQC() *hotstuff.QuorumCertificate {
	st, err := hotstuff.LoadConsensusState(s.tx)
	if err != nil || st == nil || st.LastCommittedQC.View == 0 {
		return nil
	}
	qc := st.LastCommittedQC.Clone()
	return &qc
}

// serveCommitQC answers one request: a SUCCESS payload, or an error code
// and message.
func serveCommitQC(src commitQCSource, req syncwire.CommitQCRequest) ([]byte, byte, string) {
	head := src.committedHead()
	target := req.Number
	highest := req.Hash == (types.Hash{}) && req.Number == 0
	switch {
	case highest:
		target = 0
	case req.Hash != (types.Hash{}):
		h := src.headerByHash(req.Hash)
		if h == nil || h.Number == nil {
			return nil, syncwire.CodeResourceUnavailable, "unknown hash"
		}
		target = h.Number.Uint64()
		if target > head || src.canonicalHash(target) != types.Hash(req.Hash) {
			return nil, syncwire.CodeResourceUnavailable, "hash is not committed"
		}
	default:
		if target > head || src.canonicalHash(target) == (types.Hash{}) {
			return nil, syncwire.CodeResourceUnavailable, "number is not committed"
		}
	}
	// The lowest commit QC naming a committed block at or above target:
	// the persisted one, or one embedded in a committed header above target
	// (a header carries its leader's last commit QC, so they grow along the
	// chain and the first that reaches target is the lowest).
	var best *hotstuff.QuorumCertificate
	var bestNum uint64
	consider := func(qc *hotstuff.QuorumCertificate) {
		if qc == nil || qc.View == 0 {
			return
		}
		h := src.headerByHash(qc.BlockHash)
		if h == nil || h.Number == nil {
			return
		}
		n := h.Number.Uint64()
		if n < target || n > head || src.canonicalHash(n) != qc.BlockHash {
			return
		}
		// The lowest that reaches target; for "your highest" the highest.
		if best == nil || (!highest && n < bestNum) || (highest && n > bestNum) {
			best, bestNum = qc, n
		}
	}
	if !highest {
		for n := target + 1; n <= head && n <= target+commitQCScanLimit; n++ {
			h := src.headerAt(n)
			if h == nil {
				break
			}
			qc, err := hotstuff.ExtractHeaderQC(h.Extra)
			if err != nil || qc == nil || qc.View == 0 {
				continue
			}
			consider(qc)
			if best != nil {
				break
			}
		}
	}
	consider(src.lastCommittedQC())
	if highest {
		if h := src.headerAt(head); h != nil {
			if qc, err := hotstuff.ExtractHeaderQC(h.Extra); err == nil {
				consider(qc)
			}
		}
	}
	if best == nil {
		return nil, syncwire.CodeResourceUnavailable, "no commit QC reaches the block"
	}
	wire, err := hotstuff.EncodeQC(best)
	if err != nil {
		return nil, syncwire.CodeServerError, "encode QC"
	}
	resp := syncwire.CommitQCResponse{QC: wire}
	low := target
	if highest {
		low = bestNum // "your highest": the QC and its own header
	}
	if bestNum-low+1 <= syncwire.MaxCommitQCHeaders {
		for n := bestNum; ; n-- {
			h := src.headerAt(n)
			if h == nil {
				return nil, syncwire.CodeServerError, fmt.Sprintf("canonical header %d absent", n)
			}
			enc, err := rlp.EncodeToBytes(h)
			if err != nil {
				return nil, syncwire.CodeServerError, "encode header"
			}
			resp.Headers = append(resp.Headers, enc)
			if n == low {
				break
			}
		}
	}
	payload, err := resp.Encode()
	if err != nil {
		return nil, syncwire.CodeServerError, err.Error()
	}
	return payload, syncwire.CodeSuccess, ""
}

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
		payload, code, msg = serveCommitQC(txCommitQCSource{tx: tx, head: head}, req)
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

// CommitQCReply is a decoded, shape-checked commit_qc/1 answer.
type CommitQCReply struct {
	QC      *hotstuff.QuorumCertificate
	QCWire  []byte
	Headers []*block.Header // from the QC's block down to the requested number
}

// errCommitQCFault marks a reply that contradicts itself or the request.
var errCommitQCFault = errors.New("commit_qc: provable fault")

// RequestCommitQC asks pid for the commit QC anchoring (number, hash).
func RequestCommitQC(ctx context.Context, h host.Host, pid peer.ID, req syncwire.CommitQCRequest) (*CommitQCReply, error) {
	stream, err := openV11(ctx, h, pid, syncwire.ProtocolCommitQC, req.Encode())
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = stream.Reset() })
	defer stop()
	return readCommitQC(stream, req)
}

// readCommitQC reads and shape-checks one reply: Headers[0] is the QC's
// block, the headers are a descending parent-hash chain, and (with headers)
// the lowest is the requested (number, hash). The QC signature is checked
// by the caller (VerifyCommitQCWire).
func readCommitQC(r io.Reader, req syncwire.CommitQCRequest) (*CommitQCReply, error) {
	c, err := syncwire.ReadChunk(r, false, syncwire.MaxCommitQCPayload)
	if err != nil {
		return nil, err
	}
	if c.Code != syncwire.CodeSuccess {
		return nil, v11CodeErr(c)
	}
	fault := func(format string, a ...interface{}) (*CommitQCReply, error) {
		return nil, fmt.Errorf("%w: %s", errCommitQCFault, fmt.Sprintf(format, a...))
	}
	resp, err := syncwire.DecodeCommitQCResponse(c.Payload)
	if err != nil {
		return fault("%v", err)
	}
	qc, err := hotstuff.DecodeQC(resp.QC)
	if err != nil {
		return fault("QC: %v", err)
	}
	out := &CommitQCReply{QC: qc, QCWire: resp.QC}
	for i, enc := range resp.Headers {
		hdr := new(block.Header)
		if err := rlp.DecodeBytes(enc, hdr); err != nil || hdr.Number == nil {
			return fault("header %d: %v", i, err)
		}
		if i == 0 && hdr.Hash() != qc.BlockHash {
			return fault("first header %x is not the QC's block %x", hdr.Hash(), qc.BlockHash)
		}
		if i > 0 {
			above := out.Headers[i-1]
			if hdr.Number.Uint64()+1 != above.Number.Uint64() || above.ParentHash != hdr.Hash() {
				return fault("header %d does not link", i)
			}
		}
		out.Headers = append(out.Headers, hdr)
	}
	if n := len(out.Headers); n > 0 && !(req.Number == 0 && req.Hash == (types.Hash{})) {
		low := out.Headers[n-1]
		if req.Hash != (types.Hash{}) && low.Hash() != types.Hash(req.Hash) {
			return fault("lowest header %x is not the requested %x", low.Hash(), req.Hash)
		}
		if req.Hash == (types.Hash{}) && low.Number.Uint64() != req.Number {
			return fault("lowest header %d is not the requested %d", low.Number.Uint64(), req.Number)
		}
	}
	return out, nil
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
			if errors.Is(err, errCommitQCFault) {
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
