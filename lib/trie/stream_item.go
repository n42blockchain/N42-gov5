// Copyright 2019 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty off
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package trie

// StreamItem tags each key-value pair the FlatDBTrieLoader hands to its
// receiver: an account leaf, a storage leaf, an intermediate hash in the
// account trie or in a storage trie, or the cutoff that closes a subtrie.
type StreamItem uint8

const (
	// NoItem is used to signal the end of iterator
	NoItem StreamItem = iota
	// AccountStreamItem used for marking a key-value pair in the stream as belonging to an account
	AccountStreamItem
	// StorageStreamItem used for marking a key-value pair in the stream as belonging to a storage item leaf
	StorageStreamItem
	// AHashStreamItem used for marking a key-value pair in the stream as belonging to an intermediate hash
	// within the accounts (main state trie)
	AHashStreamItem
	// SHashStreamItem used for marking a key-value pair in the stream as belonging to an intermediate hash
	// within the storage items (storage tries)
	SHashStreamItem
	// CutoffStremItem used for marking the end of the subtrie
	CutoffStreamItem
)
