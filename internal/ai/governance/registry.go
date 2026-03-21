// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package governance

import (
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// DatasetRegistry manages dataset registration and provenance tracking.
// All methods are safe for concurrent use.
type DatasetRegistry struct {
	mu       sync.RWMutex
	datasets map[types.Hash]*Dataset
	byOwner  map[types.Address][]types.Hash
	byModel  map[types.Hash][]types.Hash // modelHash -> datasetIDs
	maxItems int
}

// NewDatasetRegistry creates a new registry with the given maximum capacity.
// If maxItems <= 0 no capacity limit is enforced.
func NewDatasetRegistry(maxItems int) *DatasetRegistry {
	return &DatasetRegistry{
		datasets: make(map[types.Hash]*Dataset),
		byOwner:  make(map[types.Address][]types.Hash),
		byModel:  make(map[types.Hash][]types.Hash),
		maxItems: maxItems,
	}
}

// deriveDatasetID computes a deterministic dataset identifier from the owner
// address, name, and version by hashing owner bytes || name || version with
// Keccak256.
func deriveDatasetID(owner types.Address, name, version string) types.Hash {
	data := make([]byte, 0, types.AddressLength+len(name)+len(version))
	data = append(data, owner.Bytes()...)
	data = append(data, []byte(name)...)
	data = append(data, []byte(version)...)
	return types.BytesToHash(crypto.Keccak256(data))
}

// Register adds a new dataset to the registry. The dataset ID is derived
// deterministically from the owner address, name, and version. Returns the
// computed dataset ID on success.
//
// Errors:
//   - ErrNoCategories if categories is empty
//   - ErrRegistryFull if the registry has reached maxItems
//   - ErrDatasetExists if a dataset with the same ID is already registered
func (r *DatasetRegistry) Register(
	owner types.Address,
	name, version string,
	contentHash, metadataHash types.Hash,
	size, recordCount uint64,
	categories []EthicsCategory,
) (types.Hash, error) {
	if len(categories) == 0 {
		return types.Hash{}, ErrNoCategories
	}

	id := deriveDatasetID(owner, name, version)

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.maxItems > 0 && len(r.datasets) >= r.maxItems {
		return types.Hash{}, ErrRegistryFull
	}
	if _, exists := r.datasets[id]; exists {
		return types.Hash{}, ErrDatasetExists
	}

	// Copy categories to prevent caller mutation.
	cats := make([]EthicsCategory, len(categories))
	copy(cats, categories)

	ds := &Dataset{
		ID:           id,
		ContentHash:  contentHash,
		Owner:        owner,
		Name:         name,
		Version:      version,
		Size:         size,
		RecordCount:  recordCount,
		Categories:   cats,
		Status:       DatasetPending,
		RegisteredAt: time.Now(),
		MetadataHash: metadataHash,
	}

	r.datasets[id] = ds
	r.byOwner[owner] = append(r.byOwner[owner], id)
	return id, nil
}

// Get returns a copy of the dataset with the given ID, or false if not found.
func (r *DatasetRegistry) Get(datasetID types.Hash) (*Dataset, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ds, ok := r.datasets[datasetID]
	if !ok {
		return nil, false
	}
	return copyDataset(ds), true
}

// GetByOwner returns copies of all datasets registered by the given owner.
// Returns nil if the owner has no datasets.
func (r *DatasetRegistry) GetByOwner(owner types.Address) []*Dataset {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids, ok := r.byOwner[owner]
	if !ok || len(ids) == 0 {
		return nil
	}

	result := make([]*Dataset, 0, len(ids))
	for _, id := range ids {
		if ds, exists := r.datasets[id]; exists {
			result = append(result, copyDataset(ds))
		}
	}
	return result
}

// UpdateStatus changes the status of a registered dataset.
//
// Errors:
//   - ErrDatasetNotFound if the dataset does not exist
func (r *DatasetRegistry) UpdateStatus(datasetID types.Hash, status DatasetStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ds, ok := r.datasets[datasetID]
	if !ok {
		return ErrDatasetNotFound
	}
	ds.Status = status
	return nil
}

// LinkToModel associates a dataset with a trained model, establishing
// provenance. The same dataset can be linked to multiple models, and the
// same model can be linked to multiple datasets.
//
// Errors:
//   - ErrDatasetNotFound if the dataset does not exist
func (r *DatasetRegistry) LinkToModel(datasetID, modelHash types.Hash) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.datasets[datasetID]; !ok {
		return ErrDatasetNotFound
	}

	// Avoid duplicate links.
	for _, existing := range r.byModel[modelHash] {
		if existing == datasetID {
			return nil
		}
	}

	r.byModel[modelHash] = append(r.byModel[modelHash], datasetID)
	return nil
}

// GetModelDatasets returns the dataset IDs that have been linked to the given
// model hash. Returns nil if no datasets are linked.
func (r *DatasetRegistry) GetModelDatasets(modelHash types.Hash) []types.Hash {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := r.byModel[modelHash]
	if len(ids) == 0 {
		return nil
	}

	// Return a copy to prevent caller mutation.
	out := make([]types.Hash, len(ids))
	copy(out, ids)
	return out
}

// Count returns the number of datasets currently in the registry.
func (r *DatasetRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.datasets)
}

// PurgeFinalized removes all datasets in a terminal status (Approved,
// Rejected, or Revoked) from the registry, cleaning up the byOwner and
// byModel indices. Returns the number of datasets purged.
func (r *DatasetRegistry) PurgeFinalized() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Collect IDs to purge.
	var toPurge []types.Hash
	for id, ds := range r.datasets {
		if ds.Status == DatasetApproved || ds.Status == DatasetRejected || ds.Status == DatasetRevoked {
			toPurge = append(toPurge, id)
		}
	}

	for _, id := range toPurge {
		ds := r.datasets[id]

		// Clean up byOwner index.
		ownerIDs := r.byOwner[ds.Owner]
		filtered := ownerIDs[:0]
		for _, oid := range ownerIDs {
			if oid != id {
				filtered = append(filtered, oid)
			}
		}
		if len(filtered) == 0 {
			delete(r.byOwner, ds.Owner)
		} else {
			r.byOwner[ds.Owner] = filtered
		}

		// Clean up byModel index.
		for modelHash, dsIDs := range r.byModel {
			mFiltered := dsIDs[:0]
			for _, did := range dsIDs {
				if did != id {
					mFiltered = append(mFiltered, did)
				}
			}
			if len(mFiltered) == 0 {
				delete(r.byModel, modelHash)
			} else {
				r.byModel[modelHash] = mFiltered
			}
		}

		delete(r.datasets, id)
	}

	return len(toPurge)
}

// copyDataset returns a deep copy of a Dataset, preventing callers from
// mutating internal registry state.
func copyDataset(ds *Dataset) *Dataset {
	cp := *ds
	if len(ds.Categories) > 0 {
		cp.Categories = make([]EthicsCategory, len(ds.Categories))
		copy(cp.Categories, ds.Categories)
	}
	return &cp
}
