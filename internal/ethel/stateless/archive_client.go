package stateless

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// ArchiveSource is a FullSource that also serves witnesses: an archive node
// downloads header+body+witness to forward-execute from genesis.
type ArchiveSource interface {
	FullSource
	Witness(n uint64) ([]byte, error)
}

// ArchiveExecutor forward-executes one block into PERSISTENT state and returns
// the computed post-state root. Unlike the minimal client's witness-only replay,
// the archive executor advances a writable trie/state DB (the existing eth-el
// rebuild/replay pipeline), so it both checks receiptRoot (②, internally against
// h.ReceiptHash) and yields the real state root for the ③ check. `ancestor`
// supplies trusted BLOCKHASH ancestors. It lives in internal/ethel (it needs the
// EVM); ArchiveClient stays here so the orchestration is testable without one.
type ArchiveExecutor func(n uint64, h *block.Header, body, witness []byte, ancestor func(uint64) types.Hash) (stateRoot types.Hash, err error)

// ArchiveClient is the archive node (mode "archive", architecture doc §5): it
// downloads header+body+witness, forward-executes from genesis persisting full
// historical state, verifies every block (① header chain, ② receiptRoot inside
// the executor, ③ computed stateRoot == trusted header root), catches up to tip,
// then follows live at 12 s. It never prunes — full state + all artifacts are
// retained (the heaviest mode).
type ArchiveClient struct {
	src  ArchiveSource
	hc   *HeaderChain
	exec ArchiveExecutor
	head uint64
}

// NewArchiveClient trusts `genesis` and forward-executes via exec.
func NewArchiveClient(src ArchiveSource, genesis *block.Header, exec ArchiveExecutor) (*ArchiveClient, error) {
	if exec == nil {
		return nil, fmt.Errorf("archiveclient: nil executor")
	}
	hc, err := NewHeaderChain(genesis)
	if err != nil {
		return nil, err
	}
	return &ArchiveClient{src: src, hc: hc, exec: exec, head: genesis.Number.Uint64()}, nil
}

// Sync executes head+1..tip. Each block: extend the header chain (①), fetch its
// body+witness, execute into persistent state (② receiptRoot), and check the
// computed state root equals the trusted header root (③). Stops at the first
// failure with the last good head. Call repeatedly (e.g. on a 12 s ticker) — once
// AtTip reports true it has caught up and is following live.
func (c *ArchiveClient) Sync() (uint64, error) {
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
		wit, werr := c.src.Witness(n)
		if werr != nil {
			return c.head, fmt.Errorf("fetch witness %d: %w", n, werr)
		}
		root, eerr := c.exec(n, h, body, wit, c.ancestorHash) // ② execute + persist
		if eerr != nil {
			return c.head, fmt.Errorf("execute %d: %w", n, eerr)
		}
		trusted, ok := c.hc.TrustedStateRoot(n) // ③ computed root vs trusted header root
		if !ok {
			return c.head, fmt.Errorf("no trusted state root for %d", n)
		}
		if root != trusted {
			return c.head, fmt.Errorf("block %d state root %x != header %x", n, root[:8], trusted[:8])
		}
		c.head = n
	}
	return c.head, nil
}

// ancestorHash yields the trusted hash of block m for the BLOCKHASH opcode.
func (c *ArchiveClient) ancestorHash(m uint64) types.Hash {
	h, _ := c.hc.TrustedHash(m)
	return h
}

// AtTip reports whether the archive has caught up to the source tip (so the
// caller can treat further Syncs as live 12 s following).
func (c *ArchiveClient) AtTip() (bool, error) {
	tip, err := c.src.Head()
	if err != nil {
		return false, err
	}
	return c.head >= tip, nil
}

// Head returns the executed head (number, hash).
func (c *ArchiveClient) Head() (uint64, types.Hash) { return c.hc.Head() }
