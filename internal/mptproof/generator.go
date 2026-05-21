package mptproof

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/historicalstate"
	"github.com/n42blockchain/N42/internal/mpttrie"
)

// Generator stitches together MPT readers, a leaf source, and a
// historical-state reader to answer proof / state-as-of queries.
//
// Lifecycle: caller owns the embedded readers and must call Close on
// the Generator (which closes any readers the Generator opened) OR
// close them itself when sharing across multiple generators.
type Generator struct {
	accountsMPT *mpttrie.Reader
	storageMPT  *mpttrie.Reader
	leaves      LeafSource
	history     *historicalstate.Reader // optional; may be nil
}

// Config bundles construction parameters.
type Config struct {
	AccountsTrieDir string // <out>/accounts-mptcache from Phase A
	StorageTrieDir  string // <out>/storage-mptcache from Phase A
	HistoryDir      string // <out>/n42-history-full (optional)
	Leaves          LeafSource
}

// New opens the trie readers and returns a Generator that owns them.
func New(cfg Config) (*Generator, error) {
	if cfg.Leaves == nil {
		return nil, errors.New("mptproof: Config.Leaves is required")
	}
	a, err := mpttrie.Open(cfg.AccountsTrieDir, "AccountsTrie")
	if err != nil {
		return nil, fmt.Errorf("open accounts trie: %w", err)
	}
	s, err := mpttrie.Open(cfg.StorageTrieDir, "StoragesTrie")
	if err != nil {
		a.Close()
		return nil, fmt.Errorf("open storage trie: %w", err)
	}
	g := &Generator{accountsMPT: a, storageMPT: s, leaves: cfg.Leaves}
	if cfg.HistoryDir != "" {
		h, herr := historicalstate.Open(cfg.HistoryDir)
		if herr != nil {
			// history is optional; log via wrapped error but proceed
			a.Close()
			s.Close()
			return nil, fmt.Errorf("open history: %w", herr)
		}
		g.history = h
	}
	return g, nil
}

func (g *Generator) Close() error {
	if g.accountsMPT != nil {
		g.accountsMPT.Close()
	}
	if g.storageMPT != nil {
		g.storageMPT.Close()
	}
	if g.history != nil {
		g.history.Close()
	}
	if g.leaves != nil {
		g.leaves.Close()
	}
	return nil
}

// AccountProof bundles the data needed to verify an account inclusion
// against the latest account trie root. The wire-format encoding
// (standard RLP MPT nodes for eth_getProof) is Phase D work; this
// struct gives the caller all the raw material.
type AccountProof struct {
	Address    [20]byte
	HashedAddr [32]byte             // keccak(address)
	StateRoot  [32]byte             // root the proof verifies against
	Walk       *mpttrie.WalkResult  // path traversal data + siblings
	LeafValue  []byte               // raw account value bytes (reth Compact encoding for now)
	LeafFound  bool                 // false = account does not exist in latest plain state
}

// LatestAccountProof returns proof data for `addr` against the latest
// account-trie state root.
func (g *Generator) LatestAccountProof(addr [20]byte) (*AccountProof, error) {
	hashed := keccak(addr[:])
	var hashedArr [32]byte
	copy(hashedArr[:], hashed)

	walk, err := g.accountsMPT.Walk(nibblesOf(hashed))
	if err != nil {
		return nil, fmt.Errorf("walk accounts: %w", err)
	}

	leaf, found, err := g.leaves.AccountValue(addr)
	if err != nil {
		return nil, fmt.Errorf("leaf account value: %w", err)
	}

	root, err := g.accountsMPT.StateRoot()
	if err != nil {
		return nil, err
	}

	return &AccountProof{
		Address:    addr,
		HashedAddr: hashedArr,
		StateRoot:  root,
		Walk:       walk,
		LeafValue:  leaf,
		LeafFound:  found,
	}, nil
}

// StorageProof bundles per-slot proof data against the unified storage
// trie. (N42's storage trie is unified — composite key
// keccak(addr) || keccak(slot) — so the walk descends through both
// halves in a single MPT.)
type StorageProof struct {
	Address    [20]byte
	Slot       [32]byte
	HashedKey  [64]byte             // keccak(addr) || keccak(slot)
	StateRoot  [32]byte             // unified storage trie root
	Walk       *mpttrie.WalkResult
	LeafValue  []byte
	LeafFound  bool
}

// LatestStorageProofs returns one proof per requested slot. All
// against the latest storage trie root.
func (g *Generator) LatestStorageProofs(addr [20]byte, slots [][32]byte) ([]*StorageProof, error) {
	hashedAddr := keccak(addr[:])
	root, err := g.storageMPT.StateRoot()
	if err != nil {
		return nil, err
	}
	out := make([]*StorageProof, len(slots))
	for i, slot := range slots {
		hashedSlot := keccak(slot[:])
		var hk [64]byte
		copy(hk[:32], hashedAddr)
		copy(hk[32:], hashedSlot)

		walk, err := g.storageMPT.Walk(nibblesOf(hk[:]))
		if err != nil {
			return nil, fmt.Errorf("walk storage slot %x: %w", slot, err)
		}
		val, found, err := g.leaves.StorageValue(addr, slot)
		if err != nil {
			return nil, fmt.Errorf("leaf storage value: %w", err)
		}
		out[i] = &StorageProof{
			Address:   addr,
			Slot:      slot,
			HashedKey: hk,
			StateRoot: root,
			Walk:      walk,
			LeafValue: val,
			LeafFound: found,
		}
	}
	return out, nil
}

// LatestProof bundles both account and storage proofs in one call —
// matches the shape of eth_getProof.
type LatestProof struct {
	Account  *AccountProof
	Storages []*StorageProof
}

func (g *Generator) LatestProof(addr [20]byte, slots [][32]byte) (*LatestProof, error) {
	acct, err := g.LatestAccountProof(addr)
	if err != nil {
		return nil, err
	}
	stor, err := g.LatestStorageProofs(addr, slots)
	if err != nil {
		return nil, err
	}
	return &LatestProof{Account: acct, Storages: stor}, nil
}

// HistoricalAccountValue returns the account value at the START of
// block N (OldValue convention from internal/historicalstate). Returns
// (nil, false, nil) if no history entry ≤ blockN exists — caller's
// responsibility to decide if that means "didn't exist" or "use latest".
//
// Phase C MVP: this returns the value only; a complete Merkle proof
// at blockN requires either (a) commitment-history persistence
// (rejected in our architecture) or (b) on-demand trie subset
// recomputation (Phase D.5 follow-up). Either way the historical
// VALUE itself is correct and useful for state-as-of queries.
func (g *Generator) HistoricalAccountValue(addr [20]byte, blockN uint64) ([]byte, bool, error) {
	if g.history == nil {
		return nil, false, errors.New("mptproof: history not configured")
	}
	return g.history.AccountAsOf(addr, blockN)
}

// HistoricalStorageValue returns the storage slot value at the START
// of block N. Same caveats as HistoricalAccountValue regarding proofs.
func (g *Generator) HistoricalStorageValue(addr [20]byte, slot [32]byte, blockN uint64) ([]byte, bool, error) {
	if g.history == nil {
		return nil, false, errors.New("mptproof: history not configured")
	}
	return g.history.StorageAsOf(addr, slot, blockN)
}

// keccak is a small wrapper to avoid scattering Keccak256 init.
func keccak(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}

// nibblesOf expands bytes to nibbles (no terminator — Walk doesn't
// need the leaf terminator, only the raw key path).
func nibblesOf(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, byteVal := range b {
		out[i*2] = byteVal >> 4
		out[i*2+1] = byteVal & 0x0f
	}
	return out
}
