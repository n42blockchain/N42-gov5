// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshotjoin

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// Anchor is the certificate that makes a pivot's post-state root
// trustworthy: a commit-domain QC on block A (A >= P+k) and the canonical
// headers from A down to Low, highest first. Under deferred execution depth
// k (profile 1: k = 1, SYNC §5.3.1) the state after executing P is
// header(P+k).Root, so the anchor must reach P+k; it also carries the
// ancestors P-255..P whose hashes BLOCKHASH needs when P+1 executes.
//
// The encoding is the SUCCESS payload of /rpc/commit_qc/1 (SYNC §5.2):
//
//	qc_len u32 LE ‖ QC wire ‖ header_count u16 LE ‖ header_count × (u32 LE len ‖ RLP header)
//
// so the offline file is exactly what that protocol returns for the request
// (number = Low, hash = 0) of a server holding the QC.
type Anchor struct {
	QC      *hotstuff.QuorumCertificate
	Headers []*block.Header // Headers[0] is the QC's block; then each parent down to Low
}

// AncestorHeaders is how many headers at and below the pivot an anchor
// carries: the 256 block hashes BLOCKHASH can read while P+1 executes.
const AncestorHeaders = 256

// maxAnchorHeaders bounds a decoded anchor (u16 count on the wire).
const maxAnchorHeaders = 4096

// maxAnchorHeaderBytes bounds one header of a decoded anchor.
const maxAnchorHeaderBytes = 1 << 20

// EncodeAnchor encodes a in the commit_qc/1 payload layout.
func EncodeAnchor(a *Anchor) ([]byte, error) {
	if a == nil || a.QC == nil {
		return nil, errors.New("anchor: no QC")
	}
	if len(a.Headers) == 0 || len(a.Headers) > maxAnchorHeaders {
		return nil, fmt.Errorf("anchor: %d headers", len(a.Headers))
	}
	qc, err := hotstuff.EncodeQC(a.QC)
	if err != nil {
		return nil, fmt.Errorf("anchor: encode QC: %w", err)
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(qc)))
	out = append(out, qc...)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(a.Headers)))
	for _, h := range a.Headers {
		enc, err := rlp.EncodeToBytes(h)
		if err != nil {
			return nil, fmt.Errorf("anchor: encode header: %w", err)
		}
		out = binary.LittleEndian.AppendUint32(out, uint32(len(enc)))
		out = append(out, enc...)
	}
	return out, nil
}

// DecodeAnchor decodes the commit_qc/1 payload layout. It checks the shape
// only: DecodeAnchor does not verify the QC or the hash chain (VerifyAnchor).
func DecodeAnchor(data []byte) (*Anchor, error) {
	take := func(n int) ([]byte, error) {
		if n < 0 || len(data) < n {
			return nil, errors.New("anchor: truncated")
		}
		b := data[:n]
		data = data[n:]
		return b, nil
	}
	b, err := take(4)
	if err != nil {
		return nil, err
	}
	qcBytes, err := take(int(binary.LittleEndian.Uint32(b)))
	if err != nil {
		return nil, err
	}
	qc, err := hotstuff.DecodeQC(qcBytes)
	if err != nil {
		return nil, fmt.Errorf("anchor: decode QC: %w", err)
	}
	if b, err = take(2); err != nil {
		return nil, err
	}
	count := int(binary.LittleEndian.Uint16(b))
	if count == 0 || count > maxAnchorHeaders {
		return nil, fmt.Errorf("anchor: %d headers", count)
	}
	a := &Anchor{QC: qc, Headers: make([]*block.Header, 0, count)}
	for i := 0; i < count; i++ {
		if b, err = take(4); err != nil {
			return nil, err
		}
		n := binary.LittleEndian.Uint32(b)
		if n == 0 || n > maxAnchorHeaderBytes {
			return nil, fmt.Errorf("anchor: header %d is %d bytes", i, n)
		}
		enc, err := take(int(n))
		if err != nil {
			return nil, err
		}
		h := new(block.Header)
		if err := rlp.DecodeBytes(enc, h); err != nil {
			return nil, fmt.Errorf("anchor: decode header %d: %w", i, err)
		}
		a.Headers = append(a.Headers, h)
	}
	if len(data) != 0 {
		return nil, fmt.Errorf("anchor: %d trailing bytes", len(data))
	}
	return a, nil
}

// HeaderAt returns the anchor's header at number, if it carries one.
func (a *Anchor) HeaderAt(number uint64) *block.Header {
	if a == nil || len(a.Headers) == 0 {
		return nil
	}
	top := a.Headers[0].Number.Uint64()
	if number > top || top-number >= uint64(len(a.Headers)) {
		return nil
	}
	return a.Headers[top-number]
}

// Identity is what a profile-1 certificate is bound to (H2-v4, E10): the
// chain ID, the genesis hash, and the genesis validator set (AM5).
type Identity struct {
	ChainID     uint64
	GenesisHash types.Hash
	Validators  *hotstuff.ValidatorSet
}

// IdentityFor returns the certificate identity of a profile-1 chain.
func IdentityFor(cfg *params.ChainConfig, genesisHash types.Hash) (*Identity, error) {
	if !cfg.IsProfile1() {
		return nil, errors.New("not a profile-1 chain: the snapshot anchor is defined for profile 1 only")
	}
	if cfg.ChainID == nil || !cfg.ChainID.IsUint64() {
		return nil, errors.New("profile 1: chain ID is not a uint64")
	}
	vs := hotstuff.New(cfg.HotStuff, cfg).Profile1ValidatorSet()
	if vs == nil || vs.IsEmpty() {
		return nil, errors.New("profile 1: the genesis validator set does not parse")
	}
	return &Identity{ChainID: cfg.ChainID.Uint64(), GenesisHash: genesisHash, Validators: vs}, nil
}

// VerifyQC verifies qc as a commit-domain certificate of the chain (SYNC
// §3.3: H2-v4 commit domain, zero changesHash, the genesis set, >= 2f+1).
func (id *Identity) VerifyQC(qc *hotstuff.QuorumCertificate) error {
	if qc == nil || qc.View == 0 || qc.BlockHash == (types.Hash{}) {
		return errors.New("no commit QC (empty or genesis QC)")
	}
	return hotstuff.VerifyH2V4CommitQC(hotstuff.H2V4ChainIdentity{ChainID: id.ChainID, GenesisHash: id.GenesisHash}, qc, id.Validators)
}

// VerifyAnchor checks a against the chain and the pivot: the QC verifies in
// the commit domain, Headers[0] is its block, the headers are a contiguous
// parent-hash chain, they reach from pivot+depth down to the pivot, and a
// chain that starts at block 1 starts on this genesis. It returns the
// pivot's header and the header carrying its post-state (pivot+depth).
func (id *Identity) VerifyAnchor(a *Anchor, pivot, depth uint64) (pivotHeader, postHeader *block.Header, err error) {
	if a == nil || len(a.Headers) == 0 {
		return nil, nil, errors.New("anchor: empty")
	}
	if err := id.VerifyQC(a.QC); err != nil {
		return nil, nil, fmt.Errorf("anchor: commit QC (view %d, block %x) does not verify against the genesis validator set: %w", a.QC.View, a.QC.BlockHash, err)
	}
	if got := a.Headers[0].Hash(); got != a.QC.BlockHash {
		return nil, nil, fmt.Errorf("anchor: first header %x is not the QC's block %x", got, a.QC.BlockHash)
	}
	for i, h := range a.Headers {
		if h.Number == nil || !h.Number.IsUint64() {
			return nil, nil, fmt.Errorf("anchor: header %d has no number", i)
		}
		if i == 0 {
			continue
		}
		above := a.Headers[i-1]
		if h.Number.Uint64()+1 != above.Number.Uint64() {
			return nil, nil, fmt.Errorf("anchor: header %d is block %d below block %d", i, h.Number.Uint64(), above.Number.Uint64())
		}
		if above.ParentHash != h.Hash() {
			return nil, nil, fmt.Errorf("anchor: block %d does not link to block %d (parent %x, header %x)", above.Number.Uint64(), h.Number.Uint64(), above.ParentHash, h.Hash())
		}
	}
	low := a.Headers[len(a.Headers)-1]
	if low.Number.Uint64() == 1 && low.ParentHash != id.GenesisHash {
		return nil, nil, fmt.Errorf("anchor: block 1 does not extend this genesis (parent %x, genesis %x)", low.ParentHash, id.GenesisHash)
	}
	if low.Number.Uint64() == 0 {
		return nil, nil, errors.New("anchor: carries the genesis header")
	}
	pivotHeader = a.HeaderAt(pivot)
	postHeader = a.HeaderAt(pivot + depth)
	if pivotHeader == nil || postHeader == nil {
		return nil, nil, fmt.Errorf("anchor: covers blocks %d..%d, the pivot needs %d..%d", low.Number.Uint64(), a.Headers[0].Number.Uint64(), pivot, pivot+depth)
	}
	return pivotHeader, postHeader, nil
}

// DeferredDepth is the number of blocks between a block and the header that
// carries its post-state root: 1 on a deferred-execution chain (profile 1,
// O4), 0 before the fork.
func DeferredDepth(cfg *params.ChainConfig, nextTime uint64) uint64 {
	if cfg != nil && cfg.IsDeferredExecution(nextTime) {
		return 1
	}
	return 0
}

// ErrPostStateHeaderMissing: on a deferred chain the header that carries the
// pivot's post-state root (P+1) is not stored yet.
var ErrPostStateHeaderMissing = errors.New("the header carrying the pivot's post-state root (pivot+1) is not stored yet")

// PostStateHeader returns the header whose Root is the state after
// executing pivot: header(pivot+1) on a deferred chain (SYNC §5.3.1, k = 1),
// the pivot's own header otherwise. pivot+1 must be canonical and extend the
// pivot.
func PostStateHeader(tx kv.Getter, cfg *params.ChainConfig, pivot *block.Header) (*block.Header, error) {
	number := pivot.Number.Uint64()
	nextHash, err := rawdb.ReadCanonicalHash(tx, number+1)
	if err != nil {
		return nil, err
	}
	if nextHash == (types.Hash{}) {
		if DeferredDepth(cfg, pivot.Time) == 0 {
			return pivot, nil
		}
		return nil, fmt.Errorf("block %d: %w", number, ErrPostStateHeaderMissing)
	}
	next := rawdb.ReadHeader(tx, nextHash, number+1)
	if next == nil {
		return nil, fmt.Errorf("canonical header %d %x is absent", number+1, nextHash)
	}
	if next.ParentHash != pivot.Hash() {
		return nil, fmt.Errorf("canonical block %d does not extend pivot %d %x", number+1, number, pivot.Hash())
	}
	if DeferredDepth(cfg, next.Time) == 0 {
		return pivot, nil
	}
	return next, nil
}

// FindAnchor builds the anchor of pivot from a node's database: the lowest
// commit QC it holds -- the persisted LastCommittedQC or a QC embedded in a
// canonical header -- that names a canonical block A >= pivot+depth on the
// pivot's chain, verified with id, plus the canonical headers A..low with
// low = max(pivot-AncestorHeaders+1, 1). It returns (nil, nil) when no such
// QC is stored yet.
func FindAnchor(tx kv.Tx, id *Identity, pivot uint64, pivotHash types.Hash, depth uint64) (*Anchor, error) {
	canonical, err := rawdb.ReadCanonicalHash(tx, pivot)
	if err != nil {
		return nil, err
	}
	if canonical == (types.Hash{}) {
		return nil, nil // not committed yet
	}
	if canonical != pivotHash {
		return nil, fmt.Errorf("pivot %d %x is not canonical (canonical %x): it lost to a sibling", pivot, pivotHash, canonical)
	}
	var candidates []*hotstuff.QuorumCertificate
	if st, err := hotstuff.LoadConsensusState(tx); err == nil && st != nil && st.LastCommittedQC.View > 0 {
		qc := st.LastCommittedQC.Clone()
		candidates = append(candidates, &qc)
	}
	// QCs embedded in the canonical headers above the target: a header
	// carries its leader's last commit QC, so the first one that reaches
	// pivot+depth is normally the lowest available anchor.
	const scanLimit = 4096
	for n := pivot + depth + 1; n <= pivot+depth+scanLimit; n++ {
		h, err := rawdb.ReadCanonicalHash(tx, n)
		if err != nil {
			return nil, err
		}
		if h == (types.Hash{}) {
			break
		}
		hdr := rawdb.ReadHeader(tx, h, n)
		if hdr == nil {
			break
		}
		qc, err := hotstuff.ExtractHeaderQC(hdr.Extra)
		if err != nil || qc == nil || qc.View == 0 {
			continue
		}
		candidates = append(candidates, qc)
		if num := rawdb.ReadHeaderNumber(tx, qc.BlockHash); num != nil && *num >= pivot+depth {
			break // embedded QCs only grow along the chain
		}
	}
	var best *hotstuff.QuorumCertificate
	var bestNum uint64
	for _, qc := range candidates {
		num := rawdb.ReadHeaderNumber(tx, qc.BlockHash)
		if num == nil || *num < pivot+depth {
			continue
		}
		if c, err := rawdb.ReadCanonicalHash(tx, *num); err != nil || c != qc.BlockHash {
			continue
		}
		if best != nil && *num >= bestNum {
			continue
		}
		if id.VerifyQC(qc) != nil {
			continue
		}
		best, bestNum = qc, *num
	}
	if best == nil {
		return nil, nil
	}
	low := uint64(1)
	if pivot+1 > AncestorHeaders {
		low = pivot + 1 - AncestorHeaders
	}
	a := &Anchor{QC: best}
	for n := bestNum; ; n-- {
		h, err := rawdb.ReadCanonicalHash(tx, n)
		if err != nil {
			return nil, err
		}
		hdr := rawdb.ReadHeader(tx, h, n)
		if hdr == nil {
			return nil, fmt.Errorf("canonical header %d %x is absent", n, h)
		}
		a.Headers = append(a.Headers, hdr)
		if n == low {
			break
		}
	}
	if a.Headers[bestNum-pivot].Hash() != pivotHash {
		return nil, fmt.Errorf("anchor chain does not pass through pivot %d %x", pivot, pivotHash)
	}
	return a, nil
}
