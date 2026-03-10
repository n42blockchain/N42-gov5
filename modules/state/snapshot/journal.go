// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshot

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"google.golang.org/protobuf/proto"

	state_proto "github.com/n42blockchain/N42/api/protocol/state"
)

// SerializeDiffLayer encodes a DiffLayer into bytes for journal persistence.
//
// Format:
//
//	block_num(8) + root(32) + parent_root(32) +
//	num_accounts(4) + [address(20) + data_len(4) + data(var)]... +
//	num_deletions(4) + [address(20)]... +
//	num_storage_addrs(4) + [address(20) + num_slots(4) + [key(32) + val_len(4) + val(var)]...]...
func SerializeDiffLayer(dl *DiffLayer) ([]byte, error) {
	if dl == nil {
		return nil, fmt.Errorf("snapshot: cannot serialize nil diff layer")
	}

	// Estimate capacity.
	size := 8 + 32 + 32 + 4 + 4 + 4
	size += len(dl.accounts) * (20 + 4 + 200) // rough estimate
	size += len(dl.accountDels) * 20
	for _, slots := range dl.storage {
		size += 20 + 4
		size += len(slots) * (32 + 4 + 32)
	}
	buf := make([]byte, 0, size)

	// Block number.
	var numBuf [8]byte
	binary.BigEndian.PutUint64(numBuf[:], dl.block)
	buf = append(buf, numBuf[:]...)

	// Root.
	buf = append(buf, dl.root[:]...)

	// Parent root.
	parentRoot := types.Hash{}
	if dl.parent != nil {
		parentRoot = dl.parent.Root()
	}
	buf = append(buf, parentRoot[:]...)

	// Accounts.
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(dl.accounts)))
	buf = append(buf, countBuf[:]...)

	for addr, acc := range dl.accounts {
		buf = append(buf, addr[:]...)
		if acc == nil {
			binary.BigEndian.PutUint32(countBuf[:], 0)
			buf = append(buf, countBuf[:]...)
		} else {
			pb := acc.ToProtoMessage()
			enc, err := proto.Marshal(pb)
			if err != nil {
				return nil, fmt.Errorf("snapshot: marshal account %s: %w", addr.Hex(), err)
			}
			binary.BigEndian.PutUint32(countBuf[:], uint32(len(enc)))
			buf = append(buf, countBuf[:]...)
			buf = append(buf, enc...)
		}
	}

	// Deletions.
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(dl.accountDels)))
	buf = append(buf, countBuf[:]...)
	for addr := range dl.accountDels {
		buf = append(buf, addr[:]...)
	}

	// Storage.
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(dl.storage)))
	buf = append(buf, countBuf[:]...)
	for addr, slots := range dl.storage {
		buf = append(buf, addr[:]...)
		binary.BigEndian.PutUint32(countBuf[:], uint32(len(slots)))
		buf = append(buf, countBuf[:]...)
		for key, val := range slots {
			buf = append(buf, key[:]...)
			if val == nil {
				binary.BigEndian.PutUint32(countBuf[:], 0)
				buf = append(buf, countBuf[:]...)
			} else {
				binary.BigEndian.PutUint32(countBuf[:], uint32(len(val)))
				buf = append(buf, countBuf[:]...)
				buf = append(buf, val...)
			}
		}
	}

	return buf, nil
}

// DeserializeDiffLayer decodes a DiffLayer from journal bytes.
// The parent field is NOT set — the caller must wire it up.
func DeserializeDiffLayer(data []byte) (*DiffLayer, error) {
	if len(data) < 72+4 { // 8+32+32+4 minimum
		return nil, fmt.Errorf("snapshot: journal data too short: %d", len(data))
	}

	off := 0
	readU32 := func() (uint32, error) {
		if off+4 > len(data) {
			return 0, fmt.Errorf("snapshot: truncated at offset %d", off)
		}
		v := binary.BigEndian.Uint32(data[off:])
		off += 4
		return v, nil
	}
	readU64 := func() (uint64, error) {
		if off+8 > len(data) {
			return 0, fmt.Errorf("snapshot: truncated at offset %d", off)
		}
		v := binary.BigEndian.Uint64(data[off:])
		off += 8
		return v, nil
	}
	readBytes := func(n int) ([]byte, error) {
		if off+n > len(data) {
			return nil, fmt.Errorf("snapshot: truncated at offset %d, need %d", off, n)
		}
		b := make([]byte, n)
		copy(b, data[off:off+n])
		off += n
		return b, nil
	}

	blockNum, err := readU64()
	if err != nil {
		return nil, err
	}

	rootBytes, err := readBytes(32)
	if err != nil {
		return nil, err
	}
	var root types.Hash
	copy(root[:], rootBytes)

	// Skip parent root — caller wires parents.
	if _, err := readBytes(32); err != nil {
		return nil, err
	}

	// Accounts.
	numAccounts, err := readU32()
	if err != nil {
		return nil, err
	}
	if numAccounts > 10_000_000 {
		return nil, fmt.Errorf("snapshot: account count %d exceeds limit", numAccounts)
	}
	accounts := make(map[types.Address]*account.StateAccount, numAccounts)
	for i := uint32(0); i < numAccounts; i++ {
		addrBytes, err := readBytes(types.AddressLength)
		if err != nil {
			return nil, err
		}
		var addr types.Address
		copy(addr[:], addrBytes)

		dataLen, err := readU32()
		if err != nil {
			return nil, err
		}
		if dataLen == 0 {
			accounts[addr] = nil
			continue
		}
		accData, err := readBytes(int(dataLen))
		if err != nil {
			return nil, err
		}
		pb := new(state_proto.Account)
		if err := proto.Unmarshal(accData, pb); err != nil {
			return nil, fmt.Errorf("snapshot: unmarshal account %s: %w", addr.Hex(), err)
		}
		acc := new(account.StateAccount)
		if err := acc.FromProtoMessage(pb); err != nil {
			return nil, fmt.Errorf("snapshot: decode account %s: %w", addr.Hex(), err)
		}
		accounts[addr] = acc
	}

	// Deletions.
	numDels, err := readU32()
	if err != nil {
		return nil, err
	}
	accountDels := make(map[types.Address]struct{}, numDels)
	for i := uint32(0); i < numDels; i++ {
		addrBytes, err := readBytes(types.AddressLength)
		if err != nil {
			return nil, err
		}
		var addr types.Address
		copy(addr[:], addrBytes)
		accountDels[addr] = struct{}{}
	}

	// Storage.
	numStorageAddrs, err := readU32()
	if err != nil {
		return nil, err
	}
	storage := make(map[types.Address]map[types.Hash][]byte, numStorageAddrs)
	for i := uint32(0); i < numStorageAddrs; i++ {
		addrBytes, err := readBytes(types.AddressLength)
		if err != nil {
			return nil, err
		}
		var addr types.Address
		copy(addr[:], addrBytes)

		numSlots, err := readU32()
		if err != nil {
			return nil, err
		}
		slots := make(map[types.Hash][]byte, numSlots)
		for j := uint32(0); j < numSlots; j++ {
			keyBytes, err := readBytes(types.HashLength)
			if err != nil {
				return nil, err
			}
			var key types.Hash
			copy(key[:], keyBytes)

			valLen, err := readU32()
			if err != nil {
				return nil, err
			}
			if valLen == 0 {
				slots[key] = nil
			} else {
				val, err := readBytes(int(valLen))
				if err != nil {
					return nil, err
				}
				slots[key] = val
			}
		}
		storage[addr] = slots
	}

	dl := &DiffLayer{
		root:        root,
		block:       blockNum,
		accounts:    accounts,
		accountDels: accountDels,
		storage:     storage,
	}
	dl.memory = dl.estimateMemory()
	return dl, nil
}

// SaveJournal persists all diff layers in the tree to the SnapshotJournal table.
// Call this during graceful shutdown. Caller must hold tree lock.
func SaveJournal(tx kv.RwTx, tree *Tree) error {
	if tree == nil {
		return nil
	}

	// Clear existing journal.
	if err := rawdb.ClearSnapshotJournal(tx); err != nil {
		return fmt.Errorf("snapshot: clear journal: %w", err)
	}

	// Collect all diff layers.
	var diffLayers []*DiffLayer
	for _, layer := range tree.layers {
		if dl, ok := layer.(*DiffLayer); ok && !dl.Stale() {
			diffLayers = append(diffLayers, dl)
		}
	}

	if len(diffLayers) == 0 {
		return nil
	}

	for _, dl := range diffLayers {
		data, err := SerializeDiffLayer(dl)
		if err != nil {
			log.Warn("Snapshot journal: skip layer", "block", dl.block, "err", err)
			continue
		}
		if err := rawdb.WriteSnapshotJournal(tx, dl.block, data); err != nil {
			return fmt.Errorf("snapshot: write journal block %d: %w", dl.block, err)
		}
	}

	// Also persist the disk layer root.
	disk := tree.diskLayer
	if disk != nil {
		if err := rawdb.WriteSnapshotDiskRoot(tx, disk.root, disk.block); err != nil {
			return fmt.Errorf("snapshot: write disk root: %w", err)
		}
	}

	log.Info("Snapshot journal saved", "diff_layers", len(diffLayers))
	return nil
}

// LoadJournal reads journal entries and reconstructs diff layers on top of
// the given disk layer root. Returns the rebuilt tree or nil if no journal.
func LoadJournal(tx kv.Tx, tree *Tree) error {
	entries, err := rawdb.ReadAllSnapshotJournal(tx)
	if err != nil {
		return fmt.Errorf("snapshot: read journal: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}

	// Deserialize all diff layers.
	deserialized := make([]*DiffLayer, 0, len(entries))
	for _, entry := range entries {
		dl, err := DeserializeDiffLayer(entry.Data)
		if err != nil {
			log.Warn("Snapshot journal: skip corrupt entry", "block", entry.BlockNum, "err", err)
			continue
		}
		deserialized = append(deserialized, dl)
	}

	if len(deserialized) == 0 {
		return nil
	}

	// Wire parent pointers. Each layer's parent is found by looking up its
	// root in the tree (which initially has only the disk layer).
	tree.lock.Lock()
	defer tree.lock.Unlock()

	for _, dl := range deserialized {
		// Find parent — could be disk layer or a previously inserted diff.
		// We try the disk layer root first, then scan existing layers.
		var parent Layer
		for _, layer := range tree.layers {
			if layer.BlockNumber() == dl.block-1 || layer.Root() == dl.parent.Root() {
				parent = layer
				break
			}
		}
		if parent == nil {
			// Fallback: assign disk layer as parent.
			parent = tree.diskLayer
		}
		dl.parent = parent
		tree.layers[dl.root] = dl
	}

	log.Info("Snapshot journal loaded", "diff_layers", len(deserialized))
	return nil
}
