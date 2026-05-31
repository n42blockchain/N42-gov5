package serve

import (
	"errors"
	"fmt"

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
