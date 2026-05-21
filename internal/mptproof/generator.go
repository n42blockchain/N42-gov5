package mptproof

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/historicalstate"
	"github.com/n42blockchain/N42/internal/mpttrie"
	"github.com/n42blockchain/N42/lib/kv"
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
	// Phase G1 dense readers — opened opportunistically when unified
	// env has AccountsDense/StoragesDense populated. nil = fall back
	// to compact + leaf-source rebuild.
	accountsDense *mpttrie.DenseReader
	storagesDense *mpttrie.DenseReader
	// Phase G2 dense V2 readers (plain-key referencing). Preferred
	// over V1 when both are present — far more compact, equally fast
	// to read (one MDBX point + on-demand leaf-hash reconstruction).
	accountsDenseV2 *mpttrie.DenseReader
	storagesDenseV2 *mpttrie.DenseReader
	unifiedEnv      kv.RoDB // non-nil in unified-env mode; we close it
	leaves          LeafSource
	history         *historicalstate.Reader // optional; may be nil
}

// Config bundles construction parameters. Use EXACTLY ONE of:
//   - ChaindataDir (preferred, post-migrate): one MDBX env with both
//     AccountsTrie + StoragesTrie + Meta buckets
//   - AccountsTrieDir + StorageTrieDir (legacy, pre-migrate): two
//     separate MDBX envs
type Config struct {
	// Unified mode (preferred). Set this to point at a single MDBX dir
	// produced by cmd/n42-mpt-migrate (or any future per-block updater
	// that writes both tries to one env atomically).
	ChaindataDir string

	// Legacy two-env mode. Used when ChaindataDir is empty.
	AccountsTrieDir string
	StorageTrieDir  string

	HistoryDir string // <out>/n42-history-full (optional)
	Leaves     LeafSource
}

// New opens the trie readers and returns a Generator.
//
// ChaindataDir takes precedence over the legacy two-dir layout.
func New(cfg Config) (*Generator, error) {
	if cfg.Leaves == nil {
		return nil, errors.New("mptproof: Config.Leaves is required")
	}
	g := &Generator{leaves: cfg.Leaves}

	if cfg.ChaindataDir != "" {
		env, a, s, err := mpttrie.OpenUnifiedDB(cfg.ChaindataDir)
		if err != nil {
			return nil, fmt.Errorf("open unified chaindata: %w", err)
		}
		g.unifiedEnv = env
		g.accountsMPT = a
		g.storageMPT = s
		// Probe dense tables: keep readers only if they have data.
		// Order: V2 first (preferred), then V1 (fallback).
		aDenseV2 := mpttrie.OpenDenseShared(env, mpttrie.AccountsDenseV2Table)
		if has, herr := aDenseV2.Has(); herr == nil && has {
			g.accountsDenseV2 = aDenseV2
		}
		sDenseV2 := mpttrie.OpenDenseShared(env, mpttrie.StoragesDenseV2Table)
		if has, herr := sDenseV2.Has(); herr == nil && has {
			g.storagesDenseV2 = sDenseV2
		}
		aDense := mpttrie.OpenDenseShared(env, mpttrie.AccountsDenseTable)
		if has, herr := aDense.Has(); herr == nil && has {
			g.accountsDense = aDense
		}
		sDense := mpttrie.OpenDenseShared(env, mpttrie.StoragesDenseTable)
		if has, herr := sDense.Has(); herr == nil && has {
			g.storagesDense = sDense
		}
	} else {
		if cfg.AccountsTrieDir == "" || cfg.StorageTrieDir == "" {
			return nil, errors.New("mptproof: either ChaindataDir OR (AccountsTrieDir+StorageTrieDir) must be set")
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
		g.accountsMPT = a
		g.storageMPT = s
	}

	if cfg.HistoryDir != "" {
		h, herr := historicalstate.Open(cfg.HistoryDir)
		if herr != nil {
			g.Close()
			return nil, fmt.Errorf("open history: %w", herr)
		}
		g.history = h
	}
	return g, nil
}

// AccountsTrieRoot returns the latest accounts-trie state root
// recorded by Phase A's builder.
func (g *Generator) AccountsTrieRoot() ([32]byte, error) {
	return g.accountsMPT.StateRoot()
}

// StorageTrieRoot returns the latest (unified) storage-trie state
// root recorded by Phase A's builder.
//
// Note: this is the UNIFIED storage trie root (composite key
// keccak(addr)||keccak(slot)), not the per-account storage root.
// EIP-1186 strictly specifies per-account; N42 made an architectural
// choice (documented in archive-commitment-final-design.md) for the
// unified trie. Phase A.5 may swap if EIP-1186 strict compat needed.
func (g *Generator) StorageTrieRoot() ([32]byte, error) {
	return g.storageMPT.StateRoot()
}

// SetLeafSource swaps the active LeafSource. Used after construction
// when the caller wants to attach a more efficient LeafSource that
// needs to coordinate with the Generator's already-open MDBX env
// (see UnifiedEnv).
func (g *Generator) SetLeafSource(src LeafSource) { g.leaves = src }

// UnifiedEnv returns the MDBX env handle when the generator was
// opened in ChaindataDir mode. Returns nil for legacy two-env mode.
// Lets callers attach a LeafSource that needs to share the env to
// avoid MDBX_BUSY from a second open in the same process.
func (g *Generator) UnifiedEnv() kv.RoDB { return g.unifiedEnv }

func (g *Generator) Close() error {
	if g.accountsDenseV2 != nil {
		g.accountsDenseV2.Close()
	}
	if g.storagesDenseV2 != nil {
		g.storagesDenseV2.Close()
	}
	if g.accountsDense != nil {
		g.accountsDense.Close()
	}
	if g.storagesDense != nil {
		g.storagesDense.Close()
	}
	if g.accountsMPT != nil {
		g.accountsMPT.Close()
	}
	if g.storageMPT != nil {
		g.storageMPT.Close()
	}
	if g.unifiedEnv != nil {
		g.unifiedEnv.Close()
		g.unifiedEnv = nil
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
