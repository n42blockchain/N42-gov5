package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/block"
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
	resp, err := s.c.Get(s.base + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, nil
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
	h := new(block.Header)
	if err := h.Unmarshal(b); err != nil {
		return nil, fmt.Errorf("decode header %d: %w", n, err)
	}
	return h, nil
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
