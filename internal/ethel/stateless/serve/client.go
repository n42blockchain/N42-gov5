package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
)

// HTTPSource is a stateless.Source backed by a producer's HTTP serve endpoint —
// the client half of the transport that pairs with Handler. It implements the
// minimal client's Source (Head / Header / Anchor); GetWitness is available for
// layer ② wiring.
type HTTPSource struct {
	base string
	c    *http.Client
}

// NewHTTPSource targets a producer base URL (e.g. http://producer:8555).
func NewHTTPSource(base string) *HTTPSource {
	return &HTTPSource{
		base: strings.TrimRight(base, "/"),
		c:    &http.Client{Timeout: 15 * time.Second},
	}
}

var _ stateless.Source = (*HTTPSource)(nil)

func (s *HTTPSource) get(path string) ([]byte, error) {
	// Retry on 429/503 with bounded backoff — a real client respects the
	// producer's per-IP rate/concurrency limiter rather than failing the sync.
	backoff := 50 * time.Millisecond
	for attempt := 0; ; attempt++ {
		resp, err := s.c.Get(s.base + path)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return body, nil
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable) && attempt < 12 {
			time.Sleep(backoff)
			if backoff < 2*time.Second {
				backoff *= 2
			}
			continue
		}
		return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
}

// Head returns the producer's tip block number.
func (s *HTTPSource) Head() (uint64, error) {
	b, err := s.get("/head")
	if err != nil {
		return 0, err
	}
	var v struct {
		Number uint64 `json:"number"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return 0, fmt.Errorf("decode head: %w", err)
	}
	return v.Number, nil
}

// Header fetches and decodes block n's header.
func (s *HTTPSource) Header(n uint64) (*block.Header, error) {
	b, err := s.get(fmt.Sprintf("/header?n=%d", n))
	if err != nil {
		return nil, err
	}
	h, err := DecodeHeaderRecord(b)
	if err != nil {
		return nil, fmt.Errorf("decode header %d: %w", n, err)
	}
	return h, nil
}

// HeadersFrom fetches up to count contiguous headers starting at from, decoding
// the [4-byte-LE len || header] record stream from /headers. Returns fewer at a
// gap/tip. Far cheaper than count× single Header calls for catch-up.
func (s *HTTPSource) HeadersFrom(from, count uint64) ([]*block.Header, error) {
	b, err := s.get(fmt.Sprintf("/headers?from=%d&count=%d", from, count))
	if err != nil {
		return nil, err
	}
	var out []*block.Header
	for len(b) >= 4 {
		l := int(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
		b = b[4:]
		if l > len(b) {
			return nil, fmt.Errorf("header record length %d overflows", l)
		}
		h, err := DecodeHeaderRecord(b[:l])
		if err != nil {
			return nil, fmt.Errorf("decode header: %w", err)
		}
		out = append(out, h)
		b = b[l:]
	}
	return out, nil
}

// AnchorBytes fetches the raw compact MPT anchor proof bytes at n (the
// CompactProofFromNodes wire emitted by the producer), for structural
// VerifyProofAnchors against the trusted header stateRoot.
func (s *HTTPSource) AnchorBytes(n uint64) ([]byte, error) {
	return s.get(fmt.Sprintf("/anchor?n=%d", n))
}

// Anchor fetches and decodes the MPT anchor proof (encoded BlockProof) at n.
func (s *HTTPSource) Anchor(n uint64) (*stateless.BlockProof, error) {
	b, err := s.get(fmt.Sprintf("/anchor?n=%d", n))
	if err != nil {
		return nil, err
	}
	return stateless.DecodeBlockProof(b)
}

// GetWitness fetches block n's witness stream (for layer ② wiring).
func (s *HTTPSource) GetWitness(n uint64) ([]byte, error) {
	return s.get(fmt.Sprintf("/witness?n=%d", n))
}

// AccountProof fetches the EIP-1186 proof for addr (+ optional storage slots) at
// the producer's head and decodes it. The mobile/minimal layer-③: verify the
// returned proof via stateless.VerifyAccountInclusion against the trusted head
// stateRoot. Bounded (~KB), unlike the full-window MPT anchor.
func (s *HTTPSource) AccountProof(addr types.Address, slots []types.Hash) (*AccountProofResponse, error) {
	q := "/account-proof?addr=" + addr.Hex()
	if len(slots) > 0 {
		parts := make([]string, len(slots))
		for i, sl := range slots {
			parts[i] = sl.Hex()
		}
		q += "&slots=" + strings.Join(parts, ",")
	}
	b, err := s.get(q)
	if err != nil {
		return nil, err
	}
	res := new(AccountProofResponse)
	if err := json.Unmarshal(b, res); err != nil {
		return nil, fmt.Errorf("decode account proof: %w", err)
	}
	return res, nil
}

// Code fetches a single bytecode by keccak hash from /code (on-demand layer-②
// code distribution). Returns nil if absent. The /code response is a stream of
// [hash(32) || len(4 LE) || code] records; for one requested hash there is at
// most one record.
func (s *HTTPSource) Code(hash types.Hash) ([]byte, error) {
	b, err := s.get("/code?h=" + hash.Hex())
	if err != nil {
		return nil, err
	}
	for len(b) >= 36 {
		var h types.Hash
		copy(h[:], b[:32])
		l := int(uint32(b[32]) | uint32(b[33])<<8 | uint32(b[34])<<16 | uint32(b[35])<<24)
		if 36+l > len(b) {
			return nil, fmt.Errorf("code record length %d overflows", l)
		}
		blob := b[36 : 36+l]
		if h == hash {
			// /code ships ZSTD(code); decompress to raw bytecode.
			return codeZDecoder.DecodeAll(blob, nil)
		}
		b = b[36+l:]
	}
	return nil, nil
}

// codeZDecoder decompresses the zstd-framed code blobs from /code (DecodeAll is
// goroutine-safe).
var codeZDecoder, _ = zstd.NewReader(nil)

// AccountMultiproof fetches ONE merged account multiproof covering addrs (the
// per-block layer-③). Verify via stateless.VerifyAccountMultiproof(resp.Root,
// resp.ProofNodes, resp.Addrs) against the trusted stateRoot.
func (s *HTTPSource) AccountMultiproof(addrs []types.Address) (*AccountMultiproofResponse, error) {
	if len(addrs) == 0 {
		return &AccountMultiproofResponse{}, nil
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.Hex()
	}
	b, err := s.get("/account-multiproof?addrs=" + strings.Join(parts, ","))
	if err != nil {
		return nil, err
	}
	res := new(AccountMultiproofResponse)
	if err := json.Unmarshal(b, res); err != nil {
		return nil, fmt.Errorf("decode account multiproof: %w", err)
	}
	return res, nil
}

// FullHeader fetches block n's full canonical-RLP header (all exec fields), for
// layer-② witness replay. Distinct from Header (the compact ①-chain record).
func (s *HTTPSource) FullHeader(n uint64) (*block.Header, error) {
	b, err := s.get(fmt.Sprintf("/full-header?n=%d", n))
	if err != nil {
		return nil, err
	}
	return HeaderFromRLP(b)
}

// Witness satisfies stateless.ArchiveSource (alias of GetWitness).
func (s *HTTPSource) Witness(n uint64) ([]byte, error) { return s.GetWitness(n) }

// Body fetches block n's body, satisfying stateless.FullSource. It reads the
// /block response (4-byte-LE header length || header || body) and returns the
// trailing body bytes.
func (s *HTTPSource) Body(n uint64) ([]byte, error) {
	b, err := s.get(fmt.Sprintf("/block?n=%d", n))
	if err != nil {
		return nil, err
	}
	if len(b) < 4 {
		return nil, fmt.Errorf("block %d: short response", n)
	}
	hlen := int(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
	if 4+hlen > len(b) {
		return nil, fmt.Errorf("block %d: header length %d overflows response", n, hlen)
	}
	return b[4+hlen:], nil
}

var (
	_ stateless.FullSource    = (*HTTPSource)(nil)
	_ stateless.ArchiveSource = (*HTTPSource)(nil)
)
