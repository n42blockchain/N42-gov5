// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package v11

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/n42blockchain/N42/internal/sync/syncwire"
)

// Opener opens one request stream: it sends the framed request on proto and
// returns the reply stream (closed by the caller).
type Opener func(ctx context.Context, proto string, req []byte) (io.ReadCloser, error)

// Peer is one state-sync source: the six v1.1 requests over an Opener.
type Peer struct {
	Name string
	Open Opener
}

func (p *Peer) String() string { return p.Name }

func (p *Peer) do(ctx context.Context, proto string, req []byte, read func(io.Reader) error) error {
	rc, err := p.Open(ctx, proto, req)
	if err != nil {
		return err
	}
	defer rc.Close()
	if d, ok := ctx.Deadline(); ok {
		if s, ok := rc.(interface{ SetReadDeadline(time.Time) error }); ok {
			_ = s.SetReadDeadline(d)
		}
	}
	return read(rc)
}

// single reads one chunk that must be SUCCESS.
func single(r io.Reader, max uint64) ([]byte, error) {
	c, err := syncwire.ReadChunk(r, false, max)
	if err != nil {
		return nil, err
	}
	if c.Code != syncwire.CodeSuccess {
		return nil, &syncwire.CodeError{Code: c.Code, Msg: string(c.Payload)}
	}
	return c.Payload, nil
}

// Status asks for qmdb_status/1.
func (p *Peer) Status(ctx context.Context) (st syncwire.QMDBStatus, err error) {
	err = p.do(ctx, syncwire.ProtocolQMDBStatus, nil, func(r io.Reader) error {
		b, err := single(r, syncwire.QMDBStatusLen)
		if err != nil {
			return err
		}
		st, err = syncwire.DecodeQMDBStatus(b)
		return err
	})
	return st, err
}

// CommitQC asks for commit_qc/1 (shape-checked; the QC is unverified).
func (p *Peer) CommitQC(ctx context.Context, req syncwire.CommitQCRequest) (rep *CommitQCReply, err error) {
	err = p.do(ctx, syncwire.ProtocolCommitQC, req.Encode(), func(r io.Reader) error {
		rep, err = ReadCommitQC(r, req)
		return err
	})
	return rep, err
}

// Range asks for bodies_by_range/2.
func (p *Peer) Range(ctx context.Context, req syncwire.RangeRequest) (res *RangeV2Result, err error) {
	err = p.do(ctx, syncwire.ProtocolBodiesByRangeV2, req.Encode(), func(r io.Reader) error {
		res, err = ReadRangeV2(r, req, nil)
		return err
	})
	return res, err
}

// Manifest asks for one qmdb_manifest/1 page (unverified).
func (p *Peer) Manifest(ctx context.Context, req syncwire.ManifestRequest) (page syncwire.ManifestPage, err error) {
	err = p.do(ctx, syncwire.ProtocolQMDBManifest, req.Encode(), func(r io.Reader) error {
		b, err := single(r, syncwire.MaxManifestPayload)
		if err != nil {
			return err
		}
		page, err = syncwire.DecodeManifestPage(b)
		return err
	})
	return page, err
}

// ErrTwigsFault marks a qmdb_twigs/1 reply that contradicts its request.
var ErrTwigsFault = errors.New("qmdb_twigs: provable fault")

// Twigs asks for qmdb_twigs/1. The chunks are in order from req.FirstTwig
// (checked); a missing END returns a nil end with the chunks received.
func (p *Peer) Twigs(ctx context.Context, req syncwire.TwigsRequest) (chunks []syncwire.TwigChunk, end *syncwire.TwigsEnd, err error) {
	err = p.do(ctx, syncwire.ProtocolQMDBTwigs, req.Encode(), func(r io.Reader) error {
		chunks, end, err = ReadTwigs(r, req)
		return err
	})
	return chunks, end, err
}

// ReadTwigs reads one qmdb_twigs/1 reply.
func ReadTwigs(r io.Reader, req syncwire.TwigsRequest) ([]syncwire.TwigChunk, *syncwire.TwigsEnd, error) {
	var chunks []syncwire.TwigChunk
	for {
		c, err := syncwire.ReadChunk(r, false, syncwire.MaxTwigChunkPayload)
		if err != nil {
			if len(chunks) > 0 || errors.Is(err, io.EOF) {
				return chunks, nil, nil
			}
			return nil, nil, err
		}
		switch c.Code {
		case syncwire.CodeSuccess:
			tc, err := syncwire.DecodeTwigChunk(c.Payload)
			if err != nil {
				return chunks, nil, fmt.Errorf("%w: %v", ErrTwigsFault, err)
			}
			if want := req.FirstTwig + uint64(len(chunks)); tc.TwigID != want || len(chunks) >= int(req.TwigCount) {
				return chunks, nil, fmt.Errorf("%w: chunk %d is twig %d", ErrTwigsFault, len(chunks), tc.TwigID)
			}
			if req.BitsOnly() && len(tc.Records) != 0 {
				return chunks, nil, fmt.Errorf("%w: records in a BITS_ONLY reply", ErrTwigsFault)
			}
			chunks = append(chunks, tc)
		case syncwire.CodeEnd:
			e, err := syncwire.DecodeTwigsEnd(c.Payload)
			if err != nil {
				return chunks, nil, fmt.Errorf("%w: END: %v", ErrTwigsFault, err)
			}
			if e.NextTwig != req.FirstTwig+uint64(len(chunks)) {
				return chunks, nil, fmt.Errorf("%w: END next_twig %d after %d chunks from %d", ErrTwigsFault, e.NextTwig, len(chunks), req.FirstTwig)
			}
			if (e.Reason == syncwire.ReasonComplete) != (e.NextTwig == req.FirstTwig+uint64(req.TwigCount)) {
				return chunks, nil, fmt.Errorf("%w: END reason %d at twig %d", ErrTwigsFault, e.Reason, e.NextTwig)
			}
			return chunks, &e, nil
		default:
			if len(chunks) > 0 {
				return chunks, nil, nil
			}
			return nil, nil, &syncwire.CodeError{Code: c.Code, Msg: string(c.Payload)}
		}
	}
}

// Code asks for code_by_hash/1: one entry per hash, nil when the server
// lacks it. The bytes are unverified (the caller checks keccak).
func (p *Peer) Code(ctx context.Context, hashes []syncwire.Hash) (out [][]byte, err error) {
	req, err := syncwire.EncodeCodeRequest(hashes)
	if err != nil {
		return nil, err
	}
	err = p.do(ctx, syncwire.ProtocolCodeByHash, req, func(r io.Reader) error {
		out, err = ReadCode(r, len(hashes))
		return err
	})
	return out, err
}

// ReadCode reads n code_by_hash/1 chunks.
func ReadCode(r io.Reader, n int) ([][]byte, error) {
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		c, err := syncwire.ReadChunk(r, false, syncwire.MaxCodeBytes)
		if err != nil {
			if len(out) > 0 && errors.Is(err, io.EOF) {
				break
			}
			return out, err
		}
		switch c.Code {
		case syncwire.CodeSuccess:
			out = append(out, c.Payload)
		case syncwire.CodeResourceUnavailable:
			out = append(out, nil)
		default:
			return out, &syncwire.CodeError{Code: c.Code, Msg: string(c.Payload)}
		}
	}
	for len(out) < n {
		out = append(out, nil)
	}
	return out, nil
}

// Responder answers raw v1.1 requests from server-side sources; it backs
// loopback peers (tests, vectors) with exactly the bytes a stream carries.
type Responder struct {
	Range    RangeSource
	CommitQC func() CommitQCSource
	State    *Server
	Earliest uint64
	CodeOf   func(syncwire.Hash) []byte
}

// Respond returns the full reply bytes to one request.
func (s *Responder) Respond(proto string, req []byte) []byte {
	var out []byte
	write := func(b []byte) bool { out = append(out, b...); return true }
	invalid := func(err error) []byte {
		return syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error())
	}
	lookup := func(h syncwire.Hash) *Snapshot {
		if s.State == nil {
			return nil
		}
		return s.State.Lookup(h)
	}
	switch proto {
	case syncwire.ProtocolBodiesByRangeV2:
		r, err := syncwire.DecodeRangeRequest(req)
		if err != nil {
			return invalid(err)
		}
		ServeRangeV2(s.Range, r, write)
	case syncwire.ProtocolCommitQC:
		r, err := syncwire.DecodeCommitQCRequest(req)
		if err != nil {
			return invalid(err)
		}
		payload, code, msg := ServeCommitQC(s.CommitQC(), r)
		if code != syncwire.CodeSuccess {
			return syncwire.AppendError(nil, code, msg)
		}
		return syncwire.AppendChunk(nil, code, nil, payload)
	case syncwire.ProtocolQMDBStatus:
		if len(req) != 0 {
			return invalid(errors.New("qmdb_status takes an empty request"))
		}
		st := syncwire.QMDBStatus{EarliestBody: s.Earliest}
		if s.State != nil {
			st = s.State.Status(s.Earliest)
		}
		return syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, st.Encode())
	case syncwire.ProtocolQMDBManifest:
		r, err := syncwire.DecodeManifestRequest(req)
		if err != nil {
			return invalid(err)
		}
		payload, code, msg := ServeManifest(lookup(r.PivotHash), r)
		if code != syncwire.CodeSuccess {
			return syncwire.AppendError(nil, code, msg)
		}
		return syncwire.AppendChunk(nil, code, nil, payload)
	case syncwire.ProtocolQMDBTwigs:
		r, err := syncwire.DecodeTwigsRequest(req)
		if err != nil {
			return invalid(err)
		}
		ServeTwigs(lookup(r.PivotHash), r, write)
	case syncwire.ProtocolCodeByHash:
		hs, err := syncwire.DecodeCodeRequest(req)
		if err != nil {
			return invalid(err)
		}
		ServeCode(s.CodeOf, hs, write)
	default:
		return nil
	}
	return out
}

// LoopbackPeer is a Peer answered by a Responder in memory. tamper, when
// set, may rewrite each reply.
func LoopbackPeer(name string, s *Responder, tamper func(proto string, req, reply []byte) []byte) *Peer {
	return &Peer{Name: name, Open: func(_ context.Context, proto string, req []byte) (io.ReadCloser, error) {
		reply := s.Respond(proto, req)
		if tamper != nil {
			reply = tamper(proto, req, reply)
		}
		return io.NopCloser(bytes.NewReader(reply)), nil
	}}
}
