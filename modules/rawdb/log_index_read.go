// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package rawdb

import (
	"fmt"
	"math"

	"github.com/RoaringBitmap/roaring"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

// BlocksForAddress returns block numbers that may contain logs emitted by the
// given address in the range [from, to]. The returned slice is sorted in
// ascending order.
func BlocksForAddress(tx kv.Tx, addr types.Address, from, to uint64) ([]uint64, error) {
	return blocksForKey(tx, modules.LogAddressIndex, addr[:], from, to)
}

// BlocksForTopic returns block numbers that may contain logs with the given
// topic in the range [from, to]. The returned slice is sorted in ascending order.
func BlocksForTopic(tx kv.Tx, topic types.Hash, from, to uint64) ([]uint64, error) {
	return blocksForKey(tx, modules.LogTopicIndex, topic[:], from, to)
}

// blocksForKey reads the roaring bitmap chunks for `key` from `bucket` and
// returns all values in [from, to] as a sorted uint64 slice.
func blocksForKey(tx kv.Tx, bucket string, key []byte, from, to uint64) ([]uint64, error) {
	if from > math.MaxUint32 || to > math.MaxUint32 {
		return nil, fmt.Errorf("block range [%d, %d] exceeds uint32 for roaring bitmap", from, to)
	}

	bm, err := bitmapdb.Get(tx, bucket, key, uint32(from), uint32(to))
	if err != nil {
		return nil, err
	}

	// Trim the bitmap to [from, to].
	if from > 0 {
		bm.RemoveRange(0, from)
	}
	if to < math.MaxUint32 {
		bm.RemoveRange(to+1, uint64(math.MaxUint32)+1)
	}

	result := make([]uint64, 0, bm.GetCardinality())
	it := bm.Iterator()
	for it.HasNext() {
		result = append(result, uint64(it.Next()))
	}
	return result, nil
}

// BlocksForAddresses returns block numbers that may contain logs from any of
// the given addresses in the range [from, to]. The result is the union of all
// individual address bitmaps, sorted in ascending order.
func BlocksForAddresses(tx kv.Tx, addrs []types.Address, from, to uint64) (*roaring.Bitmap, error) {
	if from > math.MaxUint32 || to > math.MaxUint32 {
		return nil, fmt.Errorf("block range [%d, %d] exceeds uint32 for roaring bitmap", from, to)
	}
	result := roaring.New()
	for _, addr := range addrs {
		bm, err := bitmapdb.Get(tx, modules.LogAddressIndex, addr[:], uint32(from), uint32(to))
		if err != nil {
			return nil, err
		}
		result.Or(bm)
	}
	trimBitmap(result, from, to)
	return result, nil
}

// BlocksForTopics returns block numbers that may contain logs matching the
// given topic filter in the range [from, to].
//
// The topic filter follows Ethereum semantics: topics is a list of topic
// position filters. Each position can contain multiple acceptable values (OR
// within a position). An empty position is a wildcard. The result is the
// intersection across all non-empty positions.
func BlocksForTopics(tx kv.Tx, topics [][]types.Hash, from, to uint64) (*roaring.Bitmap, error) {
	if from > math.MaxUint32 || to > math.MaxUint32 {
		return nil, fmt.Errorf("block range [%d, %d] exceeds uint32 for roaring bitmap", from, to)
	}

	var result *roaring.Bitmap

	for _, sub := range topics {
		if len(sub) == 0 {
			// Wildcard position - skip (matches everything).
			continue
		}
		// OR all topics within this position.
		posResult := roaring.New()
		for _, topic := range sub {
			bm, err := bitmapdb.Get(tx, modules.LogTopicIndex, topic[:], uint32(from), uint32(to))
			if err != nil {
				return nil, err
			}
			posResult.Or(bm)
		}
		// AND across positions.
		if result == nil {
			result = posResult
		} else {
			result.And(posResult)
		}
	}

	if result == nil {
		// All positions were wildcards or no topics specified.
		return nil, nil
	}
	trimBitmap(result, from, to)
	return result, nil
}

// trimBitmap removes values outside [from, to] from the bitmap in place.
func trimBitmap(bm *roaring.Bitmap, from, to uint64) {
	if from > 0 {
		bm.RemoveRange(0, from)
	}
	if to < math.MaxUint32 {
		bm.RemoveRange(to+1, uint64(math.MaxUint32)+1)
	}
}
