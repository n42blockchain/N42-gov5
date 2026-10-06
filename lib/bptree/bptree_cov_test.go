/*
   Copyright 2022 Erigon contributors

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

package bptree

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- bulk_types.go: Keys / KeyValues Swap and String ---

func TestCovKeysSwapAndString(t *testing.T) {
	keys := Keys{10, 20, 30}
	keys.Swap(0, 2)
	assert.Equal(t, Keys{30, 20, 10}, keys)
	assert.Equal(t, "30 20 10", keys.String())

	empty := Keys{}
	assert.Equal(t, "", empty.String())
}

func TestCovKeyValuesSwapAndString(t *testing.T) {
	k1, k2, v1, v2 := Felt(1), Felt(2), Felt(100), Felt(200)
	kv := KeyValues{keys: []*Felt{&k1, &k2}, values: []*Felt{&v1, &v2}}
	kv.Swap(0, 1)
	assert.Equal(t, Felt(2), *kv.keys[0])
	assert.Equal(t, Felt(1), *kv.keys[1])
	assert.Equal(t, Felt(200), *kv.values[0])
	assert.Equal(t, Felt(100), *kv.values[1])

	s := kv.String()
	assert.True(t, strings.Contains(s, "{2, 200}"))
	assert.True(t, strings.Contains(s, "{1, 100}"))

	emptyKV := KeyValues{keys: []*Felt{}, values: []*Felt{}}
	assert.Equal(t, "", emptyKV.String())
}

// --- tree.go: String, Size, NewEmptyTree23, Graph*, WalkKeysPostOrder ---

func TestCovNewEmptyTree23(t *testing.T) {
	tree := NewEmptyTree23()
	require.NotNil(t, tree)
	assert.Nil(t, tree.root)
	// RootHash on an empty tree returns an empty byte slice.
	assert.Equal(t, []byte{}, tree.RootHash())
	valid, err := tree.IsValid()
	assert.True(t, valid)
	assert.NoError(t, err)
}

func TestCovTreeStringAndSize(t *testing.T) {
	keys := []Felt{1, 2, 3, 4, 5, 6, 7, 8}
	keyPointers, valuePointers := K2KV(keys)
	tree := NewTree23(KeyValues{keyPointers, valuePointers})

	s := tree.String()
	assert.True(t, strings.HasPrefix(s, "root="))

	// Size counts every node visited by WalkPostOrder (internal + leaf).
	assert.True(t, tree.Size() > 0)
}

func TestCovWalkKeysPostOrder(t *testing.T) {
	keys := []Felt{5, 3, 1, 4, 2, 9, 8, 7, 6}
	sortedKeys := make([]Felt, len(keys))
	copy(sortedKeys, keys)
	for i := 0; i < len(sortedKeys); i++ {
		for j := i + 1; j < len(sortedKeys); j++ {
			if sortedKeys[j] < sortedKeys[i] {
				sortedKeys[i], sortedKeys[j] = sortedKeys[j], sortedKeys[i]
			}
		}
	}
	// NewTree23 requires sorted input by key.
	keyPointers, valuePointers := K2KV(sortedKeys)
	tree := NewTree23(KeyValues{keyPointers, valuePointers})

	gotKeys := tree.WalkKeysPostOrder()
	assert.Equal(t, sortedKeys, gotKeys)
}

func TestCovRootHashInternalNode(t *testing.T) {
	// Enough keys to force the root to become an internal node, exercising
	// Node23.hashInternal via Tree23.RootHash.
	keys := make([]Felt, 50)
	for i := range keys {
		keys[i] = Felt(i)
	}
	keyPointers, valuePointers := K2KV(keys)
	tree := NewTree23(KeyValues{keyPointers, valuePointers})

	require.False(t, tree.root.isLeaf, "expected root to be internal for 50 keys")
	hash := tree.RootHash()
	assert.NotEmpty(t, hash)
}

func TestCovGraphSaveDot(t *testing.T) {
	keys := []Felt{1, 2, 3, 4, 5}
	keyPointers, valuePointers := K2KV(keys)
	tree := NewTree23(KeyValues{keyPointers, valuePointers})

	dir := t.TempDir()
	filename := filepath.Join(dir, "mygraph")
	tree.Graph(filename, true)

	data, err := os.ReadFile(filename + ".dot")
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(data), "digraph"))

	// Also exercise the nil-root branch of saveDot.
	empty := NewEmptyTree23()
	emptyFilename := filepath.Join(dir, "emptygraph")
	empty.Graph(emptyFilename, false)
	emptyData, err := os.ReadFile(emptyFilename + ".dot")
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(emptyData), "digraph"))
}

func TestCovGraphAndPicture(t *testing.T) {
	if _, err := exec.LookPath("dot"); err != nil {
		t.Skip("graphviz 'dot' executable not available")
	}
	keys := []Felt{1, 2, 3, 4, 5, 6, 7}
	keyPointers, valuePointers := K2KV(keys)
	tree := NewTree23(KeyValues{keyPointers, valuePointers})

	wd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(wd, "testdata", "graph")) })

	err = tree.GraphAndPicture("cov_test_graph")
	assert.NoError(t, err)

	err = tree.GraphAndPictureDebug("cov_test_graph_debug")
	assert.NoError(t, err)
}

// --- node.go: hasKey, howManyHashes, isValid* error branches ---

func TestCovNodeHasKeyLeaf(t *testing.T) {
	k1, k2, nextK := Felt(1), Felt(2), Felt(3)
	v1, v2, nextV := Felt(10), Felt(20), Felt(30)
	leaf := makeLeafNode([]*Felt{&k1, &k2, &nextK}, []*Felt{&v1, &v2, &nextV}, &Stats{})

	assert.True(t, leaf.hasKey(&k1))
	assert.True(t, leaf.hasKey(&k2))
	// nextKey is not a canonical key for a leaf.
	assert.False(t, leaf.hasKey(&nextK))

	missing := Felt(999)
	assert.False(t, leaf.hasKey(&missing))
}

func TestCovHowManyHashesLeafWithNilNext(t *testing.T) {
	k1 := Felt(1)
	v1 := Felt(10)
	leaf := makeLeafNode([]*Felt{&k1, nil}, []*Felt{&v1, nil}, &Stats{})
	assert.Equal(t, uint(1), leaf.howManyHashes())

	leafHash := leaf.hashLeaf()
	assert.NotEmpty(t, leafHash)
}

func TestCovHowManyHashesLeaf3KeysNilNext(t *testing.T) {
	k1, k2 := Felt(1), Felt(2)
	v1, v2 := Felt(10), Felt(20)
	leaf := makeLeafNode([]*Felt{&k1, &k2, nil}, []*Felt{&v1, &v2, nil}, &Stats{})
	assert.Equal(t, uint(3), leaf.howManyHashes())

	leafHash := leaf.hashLeaf()
	assert.NotEmpty(t, leafHash)
}

func TestCovIsValidLeafWrongKeyCount(t *testing.T) {
	k1 := Felt(1)
	v1 := Felt(10)
	leaf := makeLeafNode([]*Felt{&k1}, []*Felt{&v1}, &Stats{})
	leaf.exposed = true
	valid, err := leaf.isValidLeaf()
	assert.False(t, valid)
	assert.Error(t, err)
}

// --- key_factory.go ---

func TestCovKeyBinaryFactorySizes(t *testing.T) {
	testCases := []struct {
		keySize int
		data    []byte
	}{
		{1, []byte{0x01, 0x02, 0x01, 0x03}},
		{2, []byte{0x00, 0x01, 0x00, 0x02, 0x00, 0x01}},
		{4, []byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x02}},
		{8, []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 2}},
	}

	for _, tc := range testCases {
		factory := NewKeyBinaryFactory(tc.keySize)

		reader1 := bufio.NewReader(bytes.NewReader(tc.data))
		kv := factory.NewUniqueKeyValues(reader1)
		assert.True(t, kv.Len() > 0, "keySize=%d", tc.keySize)
		for i := 0; i < kv.Len()-1; i++ {
			assert.True(t, *kv.keys[i] <= *kv.keys[i+1])
		}

		reader2 := bufio.NewReader(bytes.NewReader(tc.data))
		keys := factory.NewUniqueKeys(reader2)
		assert.True(t, len(keys) > 0, "keySize=%d", tc.keySize)
		for i := 0; i < len(keys)-1; i++ {
			assert.True(t, keys[i] <= keys[i+1])
		}
	}
}

// --- binary_file.go ---

func TestCovBinaryFileCreateAndRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "testfile")

	bf := CreateBinaryFileByPRNG(path, 128)
	require.NotNil(t, bf)
	assert.Equal(t, int64(128), bf.Size())
	assert.True(t, strings.HasPrefix(bf.Name(), path))

	reader := bf.NewReader()
	buf := make([]byte, 128)
	n, err := reader.Read(buf)
	require.NoError(t, err)
	assert.True(t, n > 0)

	bf.Close()

	reopened := OpenBinaryFile(bf.Name())
	require.NotNil(t, reopened)
	assert.Equal(t, int64(128), reopened.Size())
	reopened.Close()
}

func TestCovBinaryFileRandomSampling(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source")
	source := CreateBinaryFileByPRNG(sourcePath, 256)
	defer source.Close()

	samplePath := filepath.Join(dir, "sample")
	sample := CreateBinaryFileByRandomSampling(samplePath, 64, source, 8)
	require.NotNil(t, sample)
	assert.Equal(t, int64(64), sample.Size())
	sample.Close()
}
