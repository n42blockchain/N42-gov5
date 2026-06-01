package serve

import (
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// ErrCapExceeded is returned when a request exceeds a per-request cap (count or
// bytes). ErrRateLimited is returned when the per-IP bandwidth budget is spent.
var (
	ErrCapExceeded = errors.New("serve: request cap exceeded")
	ErrRateLimited = errors.New("serve: bandwidth limited")
)

// Backend provides the read-only, pre-encoded artifacts a Service serves
// (RLP headers/bodies, the witness stream, the compact MPT anchor proof,
// bytecode). The producer wires this to its freezers; tests use a stub.
type Backend interface {
	Head() (num uint64, hash types.Hash, finalizedAnchor uint64, err error)
	HeaderRLP(n uint64) ([]byte, error)
	BodyRLP(n uint64) ([]byte, error)
	Witness(n uint64) ([]byte, error)
	Anchor(n uint64) ([]byte, error) // valid only at anchor heights
	Code(hash types.Hash) ([]byte, error)
	// FullHeaderRLP returns block n's header as fork-aware canonical RLP (all exec
	// fields: gasLimit/gasUsed/baseFee/time/…), for layer-② witness replay — unlike
	// the compact /header record (hash+roots only) used for the ① chain. ParentHash
	// and Bloom may be zero if the source (columnar headerc) dropped them; that does
	// not affect the gas/receiptRoot the replay checks.
	FullHeaderRLP(n uint64) ([]byte, error)
	// AccountMultiproof returns a JSON serve.AccountMultiproofResponse: ONE merged,
	// deduplicated account-trie multiproof covering all addrs (the shared upper trie
	// stored once). The per-block layer-③ artifact — prove every touched account in
	// a single request, ~30% smaller than N separate proofs. ErrNotSupported if no
	// state trie.
	AccountMultiproof(addrs []types.Address) ([]byte, error)
	// AccountProof returns a JSON-encoded account.AccProofResult (EIP-1186) for
	// addr (+ optional storage slots) at the CURRENT head state — the bounded,
	// mobile-friendly layer-③ artifact (a few KB) that replaces the full-window
	// multiproof. The client verifies it via stateless.VerifyAccountInclusion
	// against the trusted header stateRoot. Returns ErrNotSupported if the backend
	// has no state trie (freezer-only producer).
	AccountProof(addr types.Address, slots []types.Hash) ([]byte, error)
}

// ErrNotSupported is returned by AccountProof when the backend lacks a state trie.
var ErrNotSupported = errors.New("serve: account proof not supported (no state trie)")

// anchorListerBackend is the OPTIONAL capability a Backend implements to report
// its actual anchor block heights (variable cadence via the anchorc.blocks
// sidecar). Surfaced on /anchor-heights so a client verifies only real anchors.
type anchorListerBackend interface {
	AnchorHeights(from, to uint64) ([]uint64, error)
}

// GetAnchorHeights returns the producer's actual anchor heights in [from,to]
// (ascending). ErrNotSupported when the backend has no anchor index.
func (s *Service) GetAnchorHeights(ip string, from, to uint64) ([]uint64, error) {
	al, ok := s.be.(anchorListerBackend)
	if !ok {
		return nil, ErrNotSupported
	}
	hs, err := al.AnchorHeights(from, to)
	if err != nil {
		return nil, err
	}
	if cerr := s.charge(ip, 8*len(hs)); cerr != nil {
		return nil, cerr
	}
	return hs, nil
}

// AccountMultiproofResponse is the /account-multiproof wire: one merged account
// multiproof (ProofNodes, deduped) covering Addrs at block Root. The client
// verifies via stateless.VerifyAccountMultiproof(Root, ProofNodes, Addrs) — the
// proof must hash to Root, then each address walks to its leaf.
type AccountMultiproofResponse struct {
	Block      uint64
	Root       types.Hash
	Addrs      []types.Address
	ProofNodes [][]byte
}

// AccountProofResponse is the /account-proof wire: the EIP-1186 proof plus the
// block + stateRoot it anchors to, so the client can both (a) verify the proof
// reconstructs to Root (stateless.VerifyAccountInclusion) and (b) check Root ==
// the trusted header[Block].Root from its header chain.
type AccountProofResponse struct {
	Block uint64
	Root  types.Hash
	Proof *account.AccProofResult
}

// Caps bound the per-request cost so one call can't be arbitrarily expensive.
type Caps struct {
	MaxHeaders    int // max headers per GetHeaders
	MaxCodeHashes int // max code hashes per GetCode
	MaxRespBytes  int // hard ceiling on a single response's total bytes
}

// DefaultCaps: 256 headers, 64 code hashes, 8 MiB per response.
func DefaultCaps() Caps { return Caps{MaxHeaders: 256, MaxCodeHashes: 64, MaxRespBytes: 8 << 20} }

// Service is the transport-agnostic stateless serving API. Each method takes the
// caller IP for the per-IP bandwidth limiter; the per-IP REQUEST-RATE limiter
// (modules/rpc/jsonrpc.RateLimiter) is applied by the HTTP middleware ahead of
// these calls. All artifacts are immutable + content-verifiable, so a CDN can
// absorb the bulk and these methods only serve the live tip + cache misses.
type Service struct {
	be   Backend
	caps Caps
	bw   *ByteLimiter // may be nil (no bandwidth limit)
}

func NewService(be Backend, caps Caps, bw *ByteLimiter) *Service {
	return &Service{be: be, caps: caps, bw: bw}
}

// charge enforces the response-byte cap and the per-IP bandwidth budget.
func (s *Service) charge(ip string, n int) error {
	if s.caps.MaxRespBytes > 0 && n > s.caps.MaxRespBytes {
		return fmt.Errorf("%w: response %d > MaxRespBytes %d", ErrCapExceeded, n, s.caps.MaxRespBytes)
	}
	if s.bw != nil && !s.bw.Allow(ip, n) {
		return ErrRateLimited
	}
	return nil
}

// Head returns the tip (number, hash) and the latest finalized anchor block.
func (s *Service) Head() (uint64, types.Hash, uint64, error) { return s.be.Head() }

// GetHeaders returns up to count contiguous RLP headers from `from` (stops early
// at a gap/tip). Caps count + total bytes; charges the bandwidth limiter once.
func (s *Service) GetHeaders(ip string, from, count uint64) ([][]byte, error) {
	if s.caps.MaxHeaders > 0 && count > uint64(s.caps.MaxHeaders) {
		return nil, fmt.Errorf("%w: count %d > MaxHeaders %d", ErrCapExceeded, count, s.caps.MaxHeaders)
	}
	out := make([][]byte, 0, count)
	total := 0
	for i := uint64(0); i < count; i++ {
		h, err := s.be.HeaderRLP(from + i)
		if err != nil {
			break
		}
		total += len(h)
		if s.caps.MaxRespBytes > 0 && total > s.caps.MaxRespBytes {
			return nil, fmt.Errorf("%w: headers exceed MaxRespBytes", ErrCapExceeded)
		}
		out = append(out, h)
	}
	if s.bw != nil && !s.bw.Allow(ip, total) {
		return nil, ErrRateLimited
	}
	return out, nil
}

// GetBlock returns block n's RLP header + body.
func (s *Service) GetBlock(ip string, n uint64) (header, body []byte, err error) {
	h, err := s.be.HeaderRLP(n)
	if err != nil {
		return nil, nil, err
	}
	b, err := s.be.BodyRLP(n)
	if err != nil {
		return nil, nil, err
	}
	if cerr := s.charge(ip, len(h)+len(b)); cerr != nil {
		return nil, nil, cerr
	}
	return h, b, nil
}

// GetWitness returns block n's witness stream (layer ②).
func (s *Service) GetWitness(ip string, n uint64) ([]byte, error) {
	w, err := s.be.Witness(n)
	if err != nil {
		return nil, err
	}
	if cerr := s.charge(ip, len(w)); cerr != nil {
		return nil, cerr
	}
	return w, nil
}

// GetAnchor returns the compact MPT anchor proof at anchor height n (layer ③).
func (s *Service) GetAnchor(ip string, n uint64) ([]byte, error) {
	p, err := s.be.Anchor(n)
	if err != nil {
		return nil, err
	}
	if cerr := s.charge(ip, len(p)); cerr != nil {
		return nil, cerr
	}
	return p, nil
}

// GetAccountMultiproof returns the merged multiproof bytes for addrs.
func (s *Service) GetAccountMultiproof(ip string, addrs []types.Address) ([]byte, error) {
	if s.caps.MaxCodeHashes > 0 && len(addrs) > s.caps.MaxCodeHashes {
		return nil, fmt.Errorf("%w: %d addrs > cap %d", ErrCapExceeded, len(addrs), s.caps.MaxCodeHashes)
	}
	b, err := s.be.AccountMultiproof(addrs)
	if err != nil {
		return nil, err
	}
	if cerr := s.charge(ip, len(b)); cerr != nil {
		return nil, cerr
	}
	return b, nil
}

// GetFullHeader returns block n's full canonical-RLP header (layer ②).
func (s *Service) GetFullHeader(ip string, n uint64) ([]byte, error) {
	b, err := s.be.FullHeaderRLP(n)
	if err != nil {
		return nil, err
	}
	if cerr := s.charge(ip, len(b)); cerr != nil {
		return nil, cerr
	}
	return b, nil
}

// GetAccountProof returns the EIP-1186 proof bytes for addr (+ slots) at head,
// charging the byte cap + bandwidth limiter.
func (s *Service) GetAccountProof(ip string, addr types.Address, slots []types.Hash) ([]byte, error) {
	if s.caps.MaxCodeHashes > 0 && len(slots) > s.caps.MaxCodeHashes {
		return nil, fmt.Errorf("%w: %d slots > cap %d", ErrCapExceeded, len(slots), s.caps.MaxCodeHashes)
	}
	b, err := s.be.AccountProof(addr, slots)
	if err != nil {
		return nil, err
	}
	if cerr := s.charge(ip, len(b)); cerr != nil {
		return nil, cerr
	}
	return b, nil
}

// codeZEncoder is a shared, concurrency-safe zstd encoder for /code wire
// compression (EncodeAll is goroutine-safe).
var codeZEncoder, _ = zstd.NewWriter(nil)

// codeCompressedBackend is the optional fast-path a Backend can implement to ship
// the freezer's already-zstd-framed code blob directly (no decompress+recompress).
type codeCompressedBackend interface {
	CodeCompressed(hash types.Hash) ([]byte, error)
}

// GetCodeZ returns ZSTD-COMPRESSED bytecode per hash for the wire (the client
// decompresses). Bandwidth is ~45% of raw on bytecode. Prefers the backend's
// CodeCompressed passthrough (the codes-freezer already stores zstd, so no
// decompress+recompress); else compresses Code(hash). Caps + charges on the
// compressed size. Missing hashes omitted.
func (s *Service) GetCodeZ(ip string, hashes []types.Hash) (map[types.Hash][]byte, error) {
	if s.caps.MaxCodeHashes > 0 && len(hashes) > s.caps.MaxCodeHashes {
		return nil, fmt.Errorf("%w: %d hashes > MaxCodeHashes %d", ErrCapExceeded, len(hashes), s.caps.MaxCodeHashes)
	}
	cc, hasFast := s.be.(codeCompressedBackend)
	out := make(map[types.Hash][]byte, len(hashes))
	total := 0
	for _, h := range hashes {
		var blob []byte
		if hasFast {
			if z, err := cc.CodeCompressed(h); err == nil && len(z) > 0 {
				blob = z
			}
		}
		if blob == nil {
			c, err := s.be.Code(h)
			if err != nil || len(c) == 0 {
				continue
			}
			blob = codeZEncoder.EncodeAll(c, nil)
		}
		total += len(blob)
		if s.caps.MaxRespBytes > 0 && total > s.caps.MaxRespBytes {
			return nil, fmt.Errorf("%w: code exceeds MaxRespBytes", ErrCapExceeded)
		}
		out[h] = blob
	}
	if s.bw != nil && !s.bw.Allow(ip, total) {
		return nil, ErrRateLimited
	}
	return out, nil
}

// GetCode returns the requested bytecodes (content-addressed; missing hashes are
// omitted). Caps the hash count + total bytes; charges the bandwidth limiter.
func (s *Service) GetCode(ip string, hashes []types.Hash) (map[types.Hash][]byte, error) {
	if s.caps.MaxCodeHashes > 0 && len(hashes) > s.caps.MaxCodeHashes {
		return nil, fmt.Errorf("%w: %d hashes > MaxCodeHashes %d", ErrCapExceeded, len(hashes), s.caps.MaxCodeHashes)
	}
	out := make(map[types.Hash][]byte, len(hashes))
	total := 0
	for _, h := range hashes {
		c, err := s.be.Code(h)
		if err != nil || len(c) == 0 {
			continue
		}
		total += len(c)
		if s.caps.MaxRespBytes > 0 && total > s.caps.MaxRespBytes {
			return nil, fmt.Errorf("%w: code exceeds MaxRespBytes", ErrCapExceeded)
		}
		out[h] = c
	}
	if s.bw != nil && !s.bw.Allow(ip, total) {
		return nil, ErrRateLimited
	}
	return out, nil
}
