package stateless

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// FullSource is a Source that also serves block bodies. A full node archives
// header+body from genesis, so it needs bodies the minimal Source omits.
type FullSource interface {
	Source
	Body(n uint64) ([]byte, error)
}

// FullStore persists a verified (header, body) pair. The full node's durable
// genesis→tip archive sink (freezer/MDBX in production; a map in tests). body
// may be empty for a body-less block.
type FullStore func(n uint64, h *block.Header, body []byte) error

// BodyVerifier optionally binds a body to its header (e.g. recompute the
// transactionsRoot and compare to h.TxHash). nil = store the body unverified
// (header parentHash chain remains the trust backbone either way).
type BodyVerifier func(h *block.Header, body []byte) error

// FullClient is the full node (mode "full"): it follows the header chain
// (parentHash, ①) from genesis to tip and durably stores every header+body, with
// NO state, trie, or witness. It is the cold-block source that lets producers
// avoid serving historical blocks (see the architecture doc §4). Unlike the
// minimal client it does not prune and does not verify state (③) — its job is a
// complete, hash-chain-verified header+body archive.
type FullClient struct {
	src    FullSource
	hc     *HeaderChain
	store  FullStore
	verify BodyVerifier
	head   uint64
}

// NewFullClient starts a full client trusting `genesis` (block 0, or any agreed
// archive root) and following src. store persists each verified block; verify
// (optional) binds bodies to headers. The genesis block itself is stored.
func NewFullClient(src FullSource, genesis *block.Header, store FullStore, verify BodyVerifier) (*FullClient, error) {
	if store == nil {
		return nil, fmt.Errorf("fullclient: nil store")
	}
	hc, err := NewHeaderChain(genesis)
	if err != nil {
		return nil, err
	}
	c := &FullClient{src: src, hc: hc, store: store, verify: verify, head: genesis.Number.Uint64()}
	gb, _ := src.Body(c.head) // genesis body is usually empty; tolerate absence
	if err := c.persist(c.head, genesis, gb); err != nil {
		return nil, err
	}
	return c, nil
}

// Sync extends the archive from head+1 to the source tip: fetch each header,
// verify it chains (① parentHash), fetch+verify+store its body. Stops at the
// first failure, returning the last good head (the archive stays consistent).
func (c *FullClient) Sync() (uint64, error) {
	tip, err := c.src.Head()
	if err != nil {
		return c.head, err
	}
	for n := c.head + 1; n <= tip; n++ {
		h, herr := c.src.Header(n)
		if herr != nil {
			return c.head, fmt.Errorf("fetch header %d: %w", n, herr)
		}
		if err := c.hc.Extend(h); err != nil { // ① parentHash chain
			return c.head, fmt.Errorf("extend %d: %w", n, err)
		}
		body, berr := c.src.Body(n)
		if berr != nil {
			return c.head, fmt.Errorf("fetch body %d: %w", n, berr)
		}
		if err := c.persist(n, h, body); err != nil {
			return c.head, err
		}
		c.head = n
	}
	return c.head, nil
}

// persist verifies (optional) then stores a block.
func (c *FullClient) persist(n uint64, h *block.Header, body []byte) error {
	if c.verify != nil {
		if err := c.verify(h, body); err != nil {
			return fmt.Errorf("body %d verify: %w", n, err)
		}
	}
	if err := c.store(n, h, body); err != nil {
		return fmt.Errorf("store %d: %w", n, err)
	}
	return nil
}

// Head returns the archived head (number, hash).
func (c *FullClient) Head() (uint64, types.Hash) { return c.hc.Head() }

// HeaderChain exposes the trusted projection (e.g. to serve to peers).
func (c *FullClient) HeaderChain() *HeaderChain { return c.hc }
