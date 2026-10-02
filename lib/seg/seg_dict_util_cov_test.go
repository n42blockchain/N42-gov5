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
	"bufio"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestDictionaryBuilderResetAndPop covers DictionaryBuilder.Reset and the
// heap.Interface Pop method, which are only invoked indirectly (via
// processWord) in the existing tests.
func TestDictionaryBuilderResetAndPop(t *testing.T) {
	db := &DictionaryBuilder{}
	db.Reset(10)
	require.Equal(t, 0, db.Len())
	require.Equal(t, 10, db.limit)

	db.Push(&Pattern{word: []byte("a"), score: 1})
	db.Push(&Pattern{word: []byte("b"), score: 2})
	require.Equal(t, 2, db.Len())

	popped := db.Pop().(*Pattern)
	require.Equal(t, "b", string(popped.word))
	require.Equal(t, 1, db.Len())

	db.Reset(5)
	require.Equal(t, 0, db.Len())
}

// TestRingGrowthAndAccess exercises Ring.PushFront/PushBack beyond its
// initial capacity (16 cells) to hit ensureSize's growth path, plus Get and
// Truncate.
func TestRingGrowthAndAccess(t *testing.T) {
	r := NewRing()
	require.Equal(t, 0, r.Len())

	for i := 0; i < 20; i++ {
		cell := r.PushBack()
		cell.score = uint64(i)
	}
	require.Equal(t, 20, r.Len())

	for i := 0; i < 20; i++ {
		c := r.Get(i)
		require.NotNil(t, c)
		require.Equal(t, uint64(i), c.score)
	}

	require.Nil(t, r.Get(-1))
	require.Nil(t, r.Get(20))

	front := r.PushFront()
	front.score = 999
	require.Equal(t, uint64(999), r.Get(0).score)

	r.Truncate(3)
	require.Equal(t, 3, r.Len())

	r.Reset()
	require.Equal(t, 0, r.Len())
}

// TestPersistAndReadSimpleFile covers PersistDictionary and ReadSimpleFile,
// both otherwise untested helpers for dumping/reading debug dictionaries.
func TestPersistAndReadSimpleFile(t *testing.T) {
	db := &DictionaryBuilder{}
	db.Reset(10)
	db.Push(&Pattern{word: []byte("hello"), score: 5})
	db.Push(&Pattern{word: []byte("world"), score: 7})

	path := filepath.Join(t.TempDir(), "dict.txt")
	require.NoError(t, PersistDictionary(path, db))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "68656c6c6f") // hex of "hello"
	require.Contains(t, string(data), "776f726c64") // hex of "world"

	// ReadSimpleFile expects uvarint-length-prefixed raw records.
	simplePath := filepath.Join(t.TempDir(), "simple.bin")
	f, err := os.Create(simplePath)
	require.NoError(t, err)
	w := bufio.NewWriter(f)
	buf := make([]byte, binary.MaxVarintLen64)
	for _, v := range []string{"one", "two", "three"} {
		n := binary.PutUvarint(buf, uint64(len(v)))
		_, err = w.Write(buf[:n])
		require.NoError(t, err)
		_, err = w.Write([]byte(v))
		require.NoError(t, err)
	}
	require.NoError(t, w.Flush())
	require.NoError(t, f.Close())

	var got []string
	require.NoError(t, ReadSimpleFile(simplePath, func(v []byte) error {
		got = append(got, string(v))
		return nil
	}))
	require.Equal(t, []string{"one", "two", "three"}, got)
}

// TestGetterSize covers Getter.Size, a trivial accessor otherwise unused in
// existing tests.
func TestGetterSize(t *testing.T) {
	d := covBuildSimple(t, "abc", "defgh")
	g := d.MakeGetter()
	require.Equal(t, len(g.data), g.Size())
	require.Greater(t, g.Size(), 0)
}
