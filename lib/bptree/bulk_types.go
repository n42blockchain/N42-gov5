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
	"fmt"
	"strings"
)

// Keys is a sortable slice of Felt values used for bulk delete operations.
type Keys []Felt

func (keys Keys) Len() int           { return len(keys) }
func (keys Keys) Less(i, j int) bool { return keys[i] < keys[j] }
func (keys Keys) Swap(i, j int)      { keys[i], keys[j] = keys[j], keys[i] }

func (keys Keys) Contains(key Felt) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

func (keys Keys) String() string {
	b := strings.Builder{}
	for i, k := range keys {
		fmt.Fprintf(&b, "%v", k)
		if i != len(keys)-1 {
			fmt.Fprintf(&b, " ")
		}
	}
	return b.String()
}

// KeyValues is a sortable pair of key/value slices used for bulk upsert operations.
type KeyValues struct {
	keys   []*Felt
	values []*Felt
}

func (kv KeyValues) Len() int           { return len(kv.keys) }
func (kv KeyValues) Less(i, j int) bool { return *kv.keys[i] < *kv.keys[j] }

func (kv KeyValues) Swap(i, j int) {
	kv.keys[i], kv.keys[j] = kv.keys[j], kv.keys[i]
	kv.values[i], kv.values[j] = kv.values[j], kv.values[i]
}

func (kv KeyValues) String() string {
	b := strings.Builder{}
	for i, k := range kv.keys {
		v := kv.values[i]
		fmt.Fprintf(&b, "{%v, %v}", *k, *v)
		if i != len(kv.keys)-1 {
			fmt.Fprintf(&b, " ")
		}
	}
	return b.String()
}

// Stats tracks statistics for bulk tree operations.
type Stats struct {
	ExposedCount  uint
	RehashedCount uint
	CreatedCount  uint
	UpdatedCount  uint
	DeletedCount  uint
	OpeningHashes uint
	ClosingHashes uint
}
