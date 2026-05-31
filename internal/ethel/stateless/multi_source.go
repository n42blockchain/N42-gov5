package stateless

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// MultiSource fans a client's reads out across N redundant IDC producers and
// cross-checks them (architecture doc §1, §8 P-F). Producers are stateless to
// each other: the same artifact fetched from any of them must be identical
// (headers hash-match; anchors anchor to the same header root; code is
// keccak-addressed), so a faulty/malicious producer is outvoted. A MultiSource
// is itself a Source, so it drops into MinimalClient/FullClient unchanged.
//
// Trust still comes from local verification (the header chain + MPT proof);
// cross-checking is defence-in-depth that turns a single lying producer into a
// detectable disagreement rather than a silent bad value.
type MultiSource struct {
	srcs   []Source
	quorum int // M: minimum producers that must agree on a value
}

// NewMultiSource requires `quorum` of `srcs` to agree on each fetched value.
// quorum is clamped to [1, len(srcs)].
func NewMultiSource(srcs []Source, quorum int) (*MultiSource, error) {
	if len(srcs) == 0 {
		return nil, fmt.Errorf("multisource: no sources")
	}
	if quorum < 1 {
		quorum = 1
	}
	if quorum > len(srcs) {
		quorum = len(srcs)
	}
	return &MultiSource{srcs: srcs, quorum: quorum}, nil
}

// Head returns the highest tip that at least `quorum` producers agree on (same
// number AND hash). This tolerates laggards (producers a few blocks behind) while
// refusing a tip only a minority of producers claim.
func (m *MultiSource) Head() (uint64, error) {
	type hv struct {
		num  uint64
		hash types.Hash
	}
	counts := map[hv]int{}
	for _, s := range m.srcs {
		n, err := s.Head()
		if err != nil {
			continue
		}
		h, herr := s.Header(n) // need the header to learn the tip hash
		if herr != nil {
			continue
		}
		counts[hv{n, h.Hash()}]++
	}
	best := uint64(0)
	found := false
	for k, c := range counts {
		if c >= m.quorum && (!found || k.num > best) {
			best = k.num
			found = true
		}
	}
	if !found {
		return 0, fmt.Errorf("multisource: no tip reached quorum %d", m.quorum)
	}
	return best, nil
}

// Header returns block n's header once `quorum` producers return a hash-identical
// header. A producer serving a divergent header for n is outvoted; if no header
// reaches quorum the disagreement is reported, never silently resolved.
func (m *MultiSource) Header(n uint64) (*block.Header, error) {
	byHash := map[types.Hash]*block.Header{}
	counts := map[types.Hash]int{}
	for _, s := range m.srcs {
		h, err := s.Header(n)
		if err != nil {
			continue
		}
		hh := h.Hash()
		if byHash[hh] == nil {
			byHash[hh] = h
		}
		counts[hh]++
	}
	best := 0
	for hh, c := range counts {
		if c >= m.quorum {
			return byHash[hh], nil
		}
		if c > best {
			best = c
		}
	}
	return nil, fmt.Errorf("multisource: header %d reached only %d/%d agreement", n, best, m.quorum)
}

// Anchor returns block n's MPT anchor proof once `quorum` producers return a
// byte-identical compact proof. Anchors are content-bound to the header root, so
// quorum agreement plus the client's local VerifyAgainstChain is double assurance.
func (m *MultiSource) Anchor(n uint64) (*BlockProof, error) {
	counts := map[string]int{}
	byEnc := map[string]*BlockProof{}
	for _, s := range m.srcs {
		bp, err := s.Anchor(n)
		if err != nil || bp == nil {
			continue
		}
		enc := string(EncodeBlockProof(bp))
		counts[enc]++
		if byEnc[enc] == nil {
			byEnc[enc] = bp
		}
		if counts[enc] >= m.quorum {
			return bp, nil
		}
	}
	best := 0
	for _, c := range counts {
		if c > best {
			best = c
		}
	}
	return nil, fmt.Errorf("multisource: anchor %d reached only %d/%d agreement", n, best, m.quorum)
}
