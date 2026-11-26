// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package v11

import (
	"errors"
	"fmt"
	"io"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// CommitQCScanLimit bounds how many headers above the target the server
// reads looking for an embedded QC that reaches it.
const CommitQCScanLimit = 4096

// CommitQCSource is what the commit_qc/1 server reads (one consistent view).
type CommitQCSource interface {
	CommittedHead() uint64
	CanonicalHash(n uint64) types.Hash
	HeaderByHash(h types.Hash) *block.Header
	CanonicalHeader(n uint64) *block.Header
	LastCommittedQC() *hotstuff.QuorumCertificate
}

// TxCommitQCSource reads a CommitQCSource from one database transaction.
type TxCommitQCSource struct {
	Tx   kv.Tx
	Head uint64
}

func (s TxCommitQCSource) CommittedHead() uint64 { return s.Head }
func (s TxCommitQCSource) CanonicalHash(n uint64) types.Hash {
	h, _ := rawdb.ReadCanonicalHash(s.Tx, n)
	return h
}
func (s TxCommitQCSource) HeaderByHash(h types.Hash) *block.Header {
	n := rawdb.ReadHeaderNumber(s.Tx, h)
	if n == nil {
		return nil
	}
	return rawdb.ReadHeader(s.Tx, h, *n)
}
func (s TxCommitQCSource) CanonicalHeader(n uint64) *block.Header {
	h := s.CanonicalHash(n)
	if h == (types.Hash{}) {
		return nil
	}
	return rawdb.ReadHeader(s.Tx, h, n)
}
func (s TxCommitQCSource) LastCommittedQC() *hotstuff.QuorumCertificate {
	st, err := hotstuff.LoadConsensusState(s.Tx)
	if err != nil || st == nil || st.LastCommittedQC.View == 0 {
		return nil
	}
	qc := st.LastCommittedQC.Clone()
	return &qc
}

// ServeCommitQC answers one commit_qc/1 request: a SUCCESS payload, or an
// error code and message. The QC is the lowest commit QC the server holds
// (persisted, or embedded in a committed header above the target) naming the
// requested committed block or a descendant, with the headers from the QC's
// block down to the request when that is at most 256 headers (none
// otherwise). Number 0 with hash 0 asks for the highest QC (with its own
// header).
func ServeCommitQC(src CommitQCSource, req syncwire.CommitQCRequest) ([]byte, byte, string) {
	head := src.CommittedHead()
	target := req.Number
	highest := req.Hash == (types.Hash{}) && req.Number == 0
	switch {
	case highest:
		target = 0
	case req.Hash != (types.Hash{}):
		h := src.HeaderByHash(req.Hash)
		if h == nil || h.Number == nil {
			return nil, syncwire.CodeResourceUnavailable, "unknown hash"
		}
		target = h.Number.Uint64()
		if target > head || src.CanonicalHash(target) != types.Hash(req.Hash) {
			return nil, syncwire.CodeResourceUnavailable, "hash is not committed"
		}
	default:
		if target > head || src.CanonicalHash(target) == (types.Hash{}) {
			return nil, syncwire.CodeResourceUnavailable, "number is not committed"
		}
	}
	var best *hotstuff.QuorumCertificate
	var bestNum uint64
	consider := func(qc *hotstuff.QuorumCertificate) {
		if qc == nil || qc.View == 0 {
			return
		}
		h := src.HeaderByHash(qc.BlockHash)
		if h == nil || h.Number == nil {
			return
		}
		n := h.Number.Uint64()
		if n < target || n > head || src.CanonicalHash(n) != qc.BlockHash {
			return
		}
		if best == nil || (!highest && n < bestNum) || (highest && n > bestNum) {
			best, bestNum = qc, n
		}
	}
	if !highest {
		// A header carries its leader's last commit QC, so embedded QCs grow
		// along the chain: the first that reaches target is the lowest.
		for n := target + 1; n <= head && n <= target+CommitQCScanLimit; n++ {
			h := src.CanonicalHeader(n)
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
	consider(src.LastCommittedQC())
	if highest {
		if h := src.CanonicalHeader(head); h != nil {
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
		low = bestNum
	}
	if bestNum-low+1 <= syncwire.MaxCommitQCHeaders {
		for n := bestNum; ; n-- {
			h := src.CanonicalHeader(n)
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

// CommitQCReply is a decoded, shape-checked commit_qc/1 answer.
type CommitQCReply struct {
	QC      *hotstuff.QuorumCertificate
	QCWire  []byte
	Headers []*block.Header // from the QC's block down to the requested number
}

// ErrCommitQCFault marks a reply that contradicts itself or the request.
var ErrCommitQCFault = errors.New("commit_qc: provable fault")

// ReadCommitQC reads and shape-checks one reply: Headers[0] is the QC's
// block, the headers are a descending parent-hash chain, and (with headers)
// the lowest is the requested (number, hash). The QC signature is the
// caller's to verify.
func ReadCommitQC(r io.Reader, req syncwire.CommitQCRequest) (*CommitQCReply, error) {
	c, err := syncwire.ReadChunk(r, false, syncwire.MaxCommitQCPayload)
	if err != nil {
		return nil, err
	}
	if c.Code != syncwire.CodeSuccess {
		return nil, &syncwire.CodeError{Code: c.Code, Msg: string(c.Payload)}
	}
	fault := func(format string, a ...interface{}) (*CommitQCReply, error) {
		return nil, fmt.Errorf("%w: %s", ErrCommitQCFault, fmt.Sprintf(format, a...))
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
