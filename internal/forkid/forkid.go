// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package forkid computes the EIP-2124 fork identifier of a chain
// configuration. It is shared by the eth devp2p handshake and the EIP-7910
// eth_config RPC method, and depends only on params so light builds (the
// mobile SDK) can link it.
package forkid

import (
	"encoding/binary"
	"hash/crc32"
	"math/big"
	"reflect"
	"slices"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// ID is an EIP-2124 fork identifier: the CRC32 checksum of the genesis hash
// and every passed fork, and the next scheduled fork (0 if none).
type ID struct {
	Hash [4]byte
	Next uint64
}

// NewID returns the fork identifier at the given head block number and time.
func NewID(cfg *params.ChainConfig, genesisHash types.Hash, genesisTime, head, time uint64) ID {
	hash := crc32.ChecksumIEEE(genesisHash.Bytes())
	forksByBlock, forksByTime := GatherForks(cfg, genesisTime)
	for _, fork := range forksByBlock {
		if fork <= head {
			hash = checksumUpdate(hash, fork)
			continue
		}
		return ID{Hash: ChecksumToBytes(hash), Next: fork}
	}
	for _, fork := range forksByTime {
		if fork <= time {
			hash = checksumUpdate(hash, fork)
			continue
		}
		return ID{Hash: ChecksumToBytes(hash), Next: fork}
	}
	return ID{Hash: ChecksumToBytes(hash)}
}

// GatherForks returns the sorted, deduplicated block and time fork points of
// cfg, skipping block forks at genesis and time forks at or before the
// genesis time, as geth does.
func GatherForks(cfg *params.ChainConfig, genesisTime uint64) ([]uint64, []uint64) {
	if cfg == nil {
		return nil, nil
	}
	kind := reflect.TypeOf(*cfg)
	conf := reflect.ValueOf(cfg).Elem()
	var (
		forksByBlock []uint64
		forksByTime  []uint64
	)
	for i := 0; i < kind.NumField(); i++ {
		field := kind.Field(i)
		if field.Type != reflect.TypeOf((*big.Int)(nil)) {
			continue
		}
		value := conf.Field(i).Interface().(*big.Int)
		if value == nil {
			continue
		}
		switch {
		case strings.HasSuffix(field.Name, "Block"):
			forksByBlock = append(forksByBlock, value.Uint64())
		case strings.HasSuffix(field.Name, "Time"):
			forksByTime = append(forksByTime, value.Uint64())
		}
	}
	slices.Sort(forksByBlock)
	slices.Sort(forksByTime)

	forksByBlock = slices.Compact(forksByBlock)
	forksByTime = slices.Compact(forksByTime)

	for len(forksByBlock) > 0 && forksByBlock[0] == 0 {
		forksByBlock = forksByBlock[1:]
	}
	for len(forksByTime) > 0 && forksByTime[0] <= genesisTime {
		forksByTime = forksByTime[1:]
	}
	return forksByBlock, forksByTime
}

func checksumUpdate(hash uint32, fork uint64) uint32 {
	var blob [8]byte
	binary.BigEndian.PutUint64(blob[:], fork)
	return crc32.Update(hash, crc32.IEEETable, blob[:])
}

// ChecksumToBytes encodes a CRC32 checksum as the 4-byte fork hash.
func ChecksumToBytes(hash uint32) [4]byte {
	var blob [4]byte
	binary.BigEndian.PutUint32(blob[:], hash)
	return blob
}
