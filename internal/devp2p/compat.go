// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package devp2p

import (
	"encoding/binary"
	"hash/crc32"
	"math/big"
	"reflect"
	"slices"
	"strings"

	n42types "github.com/n42blockchain/N42/common/types"
	n42params "github.com/n42blockchain/N42/params"
)

func networkID(cfg *n42params.ChainConfig) uint64 {
	if cfg == nil {
		return 0
	}
	if id := n42params.NetworkIDByChainName(cfg.ChainName); id != 0 {
		return id
	}
	if cfg.ChainID != nil {
		return cfg.ChainID.Uint64()
	}
	return 0
}

func newForkID(cfg *n42params.ChainConfig, genesisHash n42types.Hash, genesisTime, head, time uint64) forkID {
	hash := crc32.ChecksumIEEE(genesisHash.Bytes())
	forksByBlock, forksByTime := gatherForks(cfg, genesisTime)
	for _, fork := range forksByBlock {
		if fork <= head {
			hash = checksumUpdate(hash, fork)
			continue
		}
		return forkID{Hash: checksumToBytes(hash), Next: fork}
	}
	for _, fork := range forksByTime {
		if fork <= time {
			hash = checksumUpdate(hash, fork)
			continue
		}
		return forkID{Hash: checksumToBytes(hash), Next: fork}
	}
	return forkID{Hash: checksumToBytes(hash)}
}

func gatherForks(cfg *n42params.ChainConfig, genesisTime uint64) ([]uint64, []uint64) {
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

	forksByBlock = dedupeUint64s(forksByBlock)
	forksByTime = dedupeUint64s(forksByTime)

	for len(forksByBlock) > 0 && forksByBlock[0] == 0 {
		forksByBlock = forksByBlock[1:]
	}
	for len(forksByTime) > 0 && forksByTime[0] <= genesisTime {
		forksByTime = forksByTime[1:]
	}
	return forksByBlock, forksByTime
}

func dedupeUint64s(values []uint64) []uint64 {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for i := 1; i < len(values); i++ {
		if values[i] == out[len(out)-1] {
			continue
		}
		out = append(out, values[i])
	}
	return out
}

func checksumUpdate(hash uint32, fork uint64) uint32 {
	var blob [8]byte
	binary.BigEndian.PutUint64(blob[:], fork)
	return crc32.Update(hash, crc32.IEEETable, blob[:])
}

func checksumToBytes(hash uint32) [4]byte {
	var blob [4]byte
	binary.BigEndian.PutUint32(blob[:], hash)
	return blob
}
