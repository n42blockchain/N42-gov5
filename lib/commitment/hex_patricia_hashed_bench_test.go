// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package commitment

import (
	"context"
	"encoding/hex"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/common/length"
)

func Benchmark_HexPatriciaHashed_Process(b *testing.B) {
	b.SetParallelism(1)

	rnd := rand.New(rand.NewSource(133777))
	keysCount := rnd.Intn(100_0000)

	// generate updates
	b.Logf("keys count: %d", keysCount)
	builder := NewUpdateBuilder()
	for i := 0; i < keysCount; i++ {
		key := make([]byte, length.Addr)
		rnd.Read(key)

		builder.Balance(hex.EncodeToString(key), rnd.Uint64())
	}
	pk, updates := builder.Build()
	b.Logf("%d keys generated", keysCount)
	ms := NewMockState(b)
	err := ms.applyPlainUpdates(pk, updates)
	require.NoError(b, err)

	hph := NewHexPatriciaHashed(length.Addr, ms)
	upds := WrapKeyUpdates(b, ModeDirect, KeyToHexNibbleHash, nil, nil)
	defer upds.Close()

	ctx := context.Background()
	for i := 0; b.Loop(); i++ {
		if i+5 >= len(pk) {
			i = 0
		}

		WrapKeyUpdatesInto(b, upds, pk[i:i+5], updates[i:i+5])
		_, err := hph.Process(ctx, upds, "", nil, WarmupConfig{})
		require.NoError(b, err)
	}
}

// whaleUpdates builds `accounts` accounts each with `slotsPerAccount` storage
// slots, mirroring the "whale contract" pattern where consecutive updates
// share the keccak(address) prefix of their hashed keys.
func whaleUpdates(tb testing.TB, seed int64, accounts, slotsPerAccount int) ([][]byte, []Update) {
	tb.Helper()
	rnd := rand.New(rand.NewSource(seed))
	builder := NewUpdateBuilder()
	for a := 0; a < accounts; a++ {
		addr := make([]byte, length.Addr)
		rnd.Read(addr)
		addrHex := hex.EncodeToString(addr)
		builder.Balance(addrHex, rnd.Uint64())
		for s := 0; s < slotsPerAccount; s++ {
			loc := make([]byte, length.Hash)
			rnd.Read(loc)
			val := make([]byte, 1+rnd.Intn(length.Hash))
			rnd.Read(val)
			builder.Storage(addrHex, hex.EncodeToString(loc), hex.EncodeToString(val))
		}
	}
	return builder.Build()
}

// benchWhale measures Updates key hashing + HexPatriciaHashed.Process for a
// fixed set of storage-heavy updates. Every iteration re-hashes all plain keys
// (TouchPlainKey) and re-walks the trie, so the per-key keccak cost is part of
// the measured work, as it is in production.
func benchWhale(b *testing.B, mode Mode, accounts, slotsPerAccount int) {
	b.SetParallelism(1)
	pk, updates := whaleUpdates(b, 0xC0FFEE, accounts, slotsPerAccount)
	ms := NewMockState(b)
	require.NoError(b, ms.applyPlainUpdates(pk, updates))

	hph := NewHexPatriciaHashed(length.Addr, ms)
	defer hph.Release()
	upds := NewUpdates(mode, b.TempDir(), KeyToHexNibbleHash)
	defer upds.Close()

	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		upds.Reset()
		WrapKeyUpdatesInto(b, upds, pk, updates)
		_, err := hph.Process(ctx, upds, "", nil, WarmupConfig{})
		require.NoError(b, err)
	}
}

func Benchmark_HexPatriciaHashed_Whale_1x1000_Direct(b *testing.B) {
	benchWhale(b, ModeDirect, 1, 1000)
}
func Benchmark_HexPatriciaHashed_Whale_1x1000_Update(b *testing.B) {
	benchWhale(b, ModeUpdate, 1, 1000)
}
func Benchmark_HexPatriciaHashed_Whale_200x100_Direct(b *testing.B) {
	benchWhale(b, ModeDirect, 200, 100)
}
func Benchmark_HexPatriciaHashed_Whale_200x100_Update(b *testing.B) {
	benchWhale(b, ModeUpdate, 200, 100)
}

// Benchmark_Updates_HashKeys_Whale isolates the plain-key hashing done by
// Updates.TouchPlainKey (ModeUpdate, btree) for 1 account x 1000 slots.
func Benchmark_Updates_HashKeys_Whale(b *testing.B) {
	pk, updates := whaleUpdates(b, 0xC0FFEE, 1, 1000)
	upds := NewUpdates(ModeUpdate, "", KeyToHexNibbleHash)
	defer upds.Close()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		upds.Reset()
		WrapKeyUpdatesInto(b, upds, pk, updates)
	}
}
