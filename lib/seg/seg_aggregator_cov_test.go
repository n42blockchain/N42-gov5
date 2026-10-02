/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package seg

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/stretchr/testify/require"
)

// TestDictAggregatorLoad covers DictAggregator.Load, a thin wrapper around
// the underlying etl.Collector.Load that is not exercised by
// DictionaryBuilderFromCollectors (which drives the collector directly).
func TestDictAggregatorLoad(t *testing.T) {
	logger := log.New()
	tmpDir := t.TempDir()
	collector := etl.NewCollector(t.Name(), tmpDir, etl.NewSortableBuffer(etl.BufferOptimalSize/2), logger)

	var scoreBuf [8]byte
	binary.BigEndian.PutUint64(scoreBuf[:], 42)
	require.NoError(t, collector.Collect([]byte("pattern"), scoreBuf[:]))

	da := &DictAggregator{collector: collector, dist: map[int]int{}}

	var gotKey []byte
	var gotVal uint64
	err := da.Load(func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		gotKey = append([]byte{}, k...)
		gotVal = binary.BigEndian.Uint64(v)
		return nil
	}, etl.TransformArgs{})
	require.NoError(t, err)

	require.Equal(t, "pattern", string(gotKey))
	require.Equal(t, uint64(42), gotVal)
}
