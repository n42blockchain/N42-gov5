// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MemProvider is the in-memory StorageProvider for slice 1 — the honest node,
// plus test-controllable fault injection (offline, corrupt). A transport-
// backed provider (messaging, signed proofs) implements the same interface
// later, exactly as the compute worker's RemoteExecutor does.

package deal

import (
	"context"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// storedDeal is one deal's chunks + manifest as held by a provider.
type storedDeal struct {
	manifest Manifest
	tree     *merkleTree
	chunks   [][]byte
}

// MemProvider holds deal chunks in memory and answers proofs.
type MemProvider struct {
	mu      sync.RWMutex
	deals   map[types.Hash]*storedDeal
	offline bool // refuse all challenges (simulates a dropped/offline node)
	corrupt bool // return a well-formed but false proof
}

// NewMemProvider creates an empty honest provider.
func NewMemProvider() *MemProvider {
	return &MemProvider{deals: make(map[types.Hash]*storedDeal)}
}

// SetOffline toggles the offline fault (challenges fail with an error).
func (p *MemProvider) SetOffline(v bool) {
	p.mu.Lock()
	p.offline = v
	p.mu.Unlock()
}

// SetCorrupt toggles the corrupt fault (returns a false-but-well-formed proof).
func (p *MemProvider) SetCorrupt(v bool) {
	p.mu.Lock()
	p.corrupt = v
	p.mu.Unlock()
}

// Store verifies the chunks against the manifest root, then keeps them. A
// provider never stores data it cannot itself commit to.
func (p *MemProvider) Store(ctx context.Context, dealID types.Hash, manifest Manifest, chunks [][]byte) error {
	if len(chunks) != manifest.ChunkCount {
		return fmt.Errorf("deal provider: chunk count %d != manifest %d", len(chunks), manifest.ChunkCount)
	}
	tree := buildMerkle(chunkHashes(chunks))
	if tree.root() != manifest.Root {
		return fmt.Errorf("deal provider: chunks do not match manifest root")
	}
	cp := make([][]byte, len(chunks))
	for i, c := range chunks {
		cp[i] = append([]byte(nil), c...)
	}
	p.mu.Lock()
	p.deals[dealID] = &storedDeal{manifest: manifest, tree: tree, chunks: cp}
	p.mu.Unlock()
	return nil
}

// Prove returns chunk i and its merkle proof, honoring fault injection.
func (p *MemProvider) Prove(ctx context.Context, dealID types.Hash, chunkIdx int) ([]byte, MerkleProof, error) {
	p.mu.RLock()
	sd, ok := p.deals[dealID]
	offline, corrupt := p.offline, p.corrupt
	p.mu.RUnlock()

	if offline {
		return nil, MerkleProof{}, fmt.Errorf("deal provider: offline")
	}
	if !ok {
		return nil, MerkleProof{}, ErrNotStored
	}
	if chunkIdx < 0 || chunkIdx >= len(sd.chunks) {
		return nil, MerkleProof{}, ErrChunkOutOfRange
	}
	proof, err := sd.tree.proof(chunkIdx)
	if err != nil {
		return nil, MerkleProof{}, err
	}
	chunk := append([]byte(nil), sd.chunks[chunkIdx]...)
	if corrupt {
		// Return the real proof but corrupted chunk bytes: well-formed shape,
		// fails verification against the committed root.
		if len(chunk) > 0 {
			chunk[0] ^= 0xFF
		} else {
			chunk = []byte{0xFF}
		}
	}
	return chunk, proof, nil
}

// Retrieve returns all chunks (repair source). Fails if offline or absent.
func (p *MemProvider) Retrieve(ctx context.Context, dealID types.Hash) ([][]byte, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.offline {
		return nil, fmt.Errorf("deal provider: offline")
	}
	sd, ok := p.deals[dealID]
	if !ok {
		return nil, ErrNotStored
	}
	out := make([][]byte, len(sd.chunks))
	for i, c := range sd.chunks {
		out[i] = append([]byte(nil), c...)
	}
	return out, nil
}

// Drop releases a deal's storage.
func (p *MemProvider) Drop(dealID types.Hash) {
	p.mu.Lock()
	delete(p.deals, dealID)
	p.mu.Unlock()
}
