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
	"unsafe"
)

// Node23 represents a node in a 2-3 tree.
type Node23 struct {
	children []*Node23
	keys     []*Felt
	values   []*Felt
	isLeaf   bool
	exposed  bool
	updated  bool
}

func (n *Node23) String() string {
	s := fmt.Sprintf("{%p isLeaf=%t keys=%v-%v children=[", n, n.isLeaf, deref(n.keys), n.keys)
	for i, child := range n.children {
		s += fmt.Sprintf("%p", child)
		if i != len(n.children)-1 {
			s += " "
		}
	}
	s += "]}"
	return s
}

func makeInternalNode(children []*Node23, keys []*Felt, stats *Stats) *Node23 {
	stats.CreatedCount++
	return &Node23{
		isLeaf:   false,
		children: children,
		keys:     keys,
		values:   make([]*Felt, 0),
		exposed:  true,
		updated:  true,
	}
}

func makeLeafNode(keys, values []*Felt, stats *Stats) *Node23 {
	ensure(len(keys) > 0, "number of keys is zero")
	ensure(len(keys) == len(values), "keys and values have different cardinality")
	stats.CreatedCount++
	return &Node23{
		isLeaf:   true,
		children: make([]*Node23, 0),
		keys:     keys,
		values:   values,
		exposed:  true,
		updated:  true,
	}
}

func makeEmptyLeafNode() *Node23 {
	return makeLeafNode(make([]*Felt, 1), make([]*Felt, 1), &Stats{})
}

func promote(nodes []*Node23, intermediateKeys []*Felt, stats *Stats) *Node23 {
	if len(nodes) > 3 {
		promotedNodes := make([]*Node23, 0)
		promotedKeys := make([]*Felt, 0)
		for len(nodes) > 3 {
			promotedNodes = append(promotedNodes, makeInternalNode(nodes[:2], intermediateKeys[:1], stats))
			nodes = nodes[2:]
			promotedKeys = append(promotedKeys, intermediateKeys[1])
			intermediateKeys = intermediateKeys[2:]
		}
		promotedNodes = append(promotedNodes, makeInternalNode(nodes, intermediateKeys, stats))
		return promote(promotedNodes, promotedKeys, stats)
	}
	return makeInternalNode(nodes, intermediateKeys, stats)
}

// Walker is a function type for tree traversal callbacks.
type Walker func(*Node23) interface{}

// --- Accessor methods ---

func (n *Node23) keyCount() int      { return len(n.keys) }
func (n *Node23) childrenCount() int  { return len(n.children) }
func (n *Node23) valueCount() int     { return len(n.values) }

func (n *Node23) firstKey() *Felt {
	ensure(len(n.keys) > 0, "firstKey: node has no key")
	return n.keys[0]
}

func (n *Node23) firstValue() *Felt {
	ensure(len(n.values) > 0, "firstValue: node has no value")
	return n.values[0]
}

func (n *Node23) firstChild() *Node23 {
	ensure(len(n.children) > 0, "firstChild: node has no children")
	return n.children[0]
}

func (n *Node23) lastChild() *Node23 {
	ensure(len(n.children) > 0, "lastChild: node has no children")
	return n.children[len(n.children)-1]
}

func (n *Node23) firstLeaf() *Node23 {
	current := n
	for !current.isLeaf {
		current = current.firstChild()
	}
	return current
}

func (n *Node23) lastLeaf() *Node23 {
	current := n
	for !current.isLeaf {
		current = current.lastChild()
	}
	return current
}

func (n *Node23) nextKey() *Felt {
	ensure(len(n.keys) > 0, "nextKey: node has no key")
	return n.keys[len(n.keys)-1]
}

func (n *Node23) nextValue() *Felt {
	ensure(len(n.values) > 0, "nextValue: node has no value")
	return n.values[len(n.values)-1]
}

func (n *Node23) rawPointer() uintptr {
	return uintptr(unsafe.Pointer(n))
}

func (n *Node23) setNextKey(nextKey *Felt, stats *Stats) {
	ensure(len(n.keys) > 0, "setNextKey: node has no key")
	n.keys[len(n.keys)-1] = nextKey
	if !n.exposed {
		n.exposed = true
		stats.ExposedCount++
		stats.OpeningHashes += n.howManyHashes()
	}
	n.updated = true
	stats.UpdatedCount++
}

func (n *Node23) canonicalKeys() []Felt {
	if n.isLeaf {
		ensure(len(n.keys) > 0, "canonicalKeys: node has no key")
		return deref(n.keys[:len(n.keys)-1])
	}
	return deref(n.keys)
}

func (n *Node23) hasKey(targetKey *Felt) bool {
	var keys []*Felt
	if n.isLeaf {
		ensure(len(n.keys) > 0, "hasKey: node has no key")
		keys = n.keys[:len(n.keys)-1]
	} else {
		keys = n.keys
	}
	for _, key := range keys {
		if *key == *targetKey {
			return true
		}
	}
	return false
}

func (n *Node23) isEmpty() bool {
	if n.isLeaf {
		return n.keyCount() == 1
	}
	return n.childrenCount() == 0
}

func (n *Node23) height() int {
	if n.isLeaf {
		return 1
	}
	ensure(len(n.children) > 0, "height: internal node has zero children")
	return n.children[0].height() + 1
}

// --- Traversal methods ---

func (n *Node23) keysInLevelOrder() []Felt {
	keysByLevel := make([]Felt, 0)
	for i := 0; i < n.height(); i++ {
		keysByLevel = append(keysByLevel, n.keysByLevel(i)...)
	}
	return keysByLevel
}

func (n *Node23) keysByLevel(level int) []Felt {
	if level == 0 {
		return n.canonicalKeys()
	}
	levelKeys := make([]Felt, 0)
	for _, child := range n.children {
		levelKeys = append(levelKeys, child.keysByLevel(level-1)...)
	}
	return levelKeys
}

func (n *Node23) walkPostOrder(w Walker) []interface{} {
	items := make([]interface{}, 0)
	if !n.isLeaf {
		for _, child := range n.children {
			items = append(items, child.walkPostOrder(w)...)
		}
	}
	items = append(items, w(n))
	return items
}

func (n *Node23) walkNodesPostOrder() []*Node23 {
	nodeItems := n.walkPostOrder(func(n *Node23) interface{} { return n })
	nodes := make([]*Node23, len(nodeItems))
	for i := range nodeItems {
		nodes[i] = nodeItems[i].(*Node23)
	}
	return nodes
}

// --- State management ---

func (n *Node23) reset() {
	n.exposed = false
	n.updated = false
	if !n.isLeaf {
		for _, child := range n.children {
			child.reset()
		}
	}
}

// --- Validation methods ---

func (n *Node23) isValid() (bool, error) {
	ensure(n.exposed || !n.updated, "isValid: node is not exposed but updated")
	if n.isLeaf {
		return n.isValidLeaf()
	}
	return n.isValidInternal()
}

func (n *Node23) isValidLeaf() (bool, error) {
	ensure(n.isLeaf, "isValidLeaf: node is not leaf")
	if n.childrenCount() != 0 {
		return false, fmt.Errorf("invalid %d children in %v", n.childrenCount(), n)
	}
	return n.keyCount() == 1+1 || n.keyCount() == 2+1, fmt.Errorf("invalid %d keys in %v", n.keyCount(), n)
}

func (n *Node23) isValidInternal() (bool, error) {
	ensure(!n.isLeaf, "isValidInternal: node is leaf")

	if n.keyCount() != 1 && n.keyCount() != 2 {
		return false, fmt.Errorf("invalid %d keys in %v", n.keyCount(), n)
	}
	if n.keyCount() == 1 && n.childrenCount() != 2 {
		return false, fmt.Errorf("invalid %d keys %d children in %v", n.keyCount(), n.childrenCount(), n)
	}
	if n.keyCount() == 2 && n.childrenCount() != 3 {
		return false, fmt.Errorf("invalid %d children in %v", n.keyCount(), n)
	}

	subtree := n.walkNodesPostOrder()

	// Check that each internal node has unique keys corresponding to leaf next keys
	for _, key := range n.keys {
		hasNextKey := false
		for _, node := range subtree {
			if !node.isLeaf {
				if node != n && node.hasKey(key) {
					return false, fmt.Errorf("internal key %d not unique", *key)
				}
				continue
			}
			leafNextKey := node.nextKey()
			if leafNextKey != nil && *key == *leafNextKey {
				hasNextKey = true
			}
		}
		if !hasNextKey {
			return false, fmt.Errorf("internal key %d not present in next keys", *key)
		}
	}

	// Check that leaves in subtree are chained together (next key -> first key)
	for i, node := range subtree {
		if !node.isLeaf {
			if i == len(subtree)-1 {
				continue
			}
			previous, next := subtree[i], subtree[i+1]
			if previous.isLeaf && next.isLeaf {
				if previous.nextKey() != next.firstKey() {
					return false, fmt.Errorf("nodes %v and %v not chained by next key", previous, next)
				}
			}
			continue
		}
	}

	for i := len(n.children) - 1; i >= 0; i-- {
		childValid, err := n.children[i].isValid()
		if !childValid {
			return false, fmt.Errorf("invalid child %v in %v, error: %w", n.children[i], n, err)
		}
	}
	return true, nil
}

// --- Hash methods ---

func (n *Node23) howManyHashes() uint {
	if n.isLeaf {
		switch n.keyCount() {
		case 2:
			if n.keys[1] == nil {
				return 1
			}
			return 2
		case 3:
			if n.keys[2] == nil {
				return 3
			}
			return 4
		default:
			ensure(false, fmt.Sprintf("howManyHashes: unexpected keyCount=%d\n", n.keyCount()))
			return 0
		}
	}

	switch n.childrenCount() {
	case 2:
		return 1
	case 3:
		return 2
	default:
		ensure(false, fmt.Sprintf("howManyHashes: unexpected childrenCount=%d\n", n.childrenCount()))
		return 0
	}
}

func (n *Node23) hashNode() []byte {
	if n.isLeaf {
		return n.hashLeaf()
	}
	return n.hashInternal()
}

func (n *Node23) hashLeaf() []byte {
	ensure(n.isLeaf, "hashLeaf: node is not leaf")
	ensure(n.valueCount() == n.keyCount(), "hashLeaf: insufficient number of values")
	switch n.keyCount() {
	case 2:
		k, nextKey, v := *n.keys[0], n.keys[1], *n.values[0]
		h := hash2(k.Binary(), v.Binary())
		if nextKey == nil {
			return h
		}
		return hash2(h, (*nextKey).Binary())
	case 3:
		k1, k2, nextKey, v1, v2 := *n.keys[0], *n.keys[1], n.keys[2], *n.values[0], *n.values[1]
		h12 := hash2(hash2(k1.Binary(), v1.Binary()), hash2(k2.Binary(), v2.Binary()))
		if nextKey == nil {
			return h12
		}
		return hash2(h12, (*nextKey).Binary())
	default:
		ensure(false, fmt.Sprintf("hashLeaf: unexpected keyCount=%d\n", n.keyCount()))
		return []byte{}
	}
}

func (n *Node23) hashInternal() []byte {
	ensure(!n.isLeaf, "hashInternal: node is not internal")
	switch n.childrenCount() {
	case 2:
		return hash2(n.children[0].hashNode(), n.children[1].hashNode())
	case 3:
		return hash2(hash2(n.children[0].hashNode(), n.children[1].hashNode()), n.children[2].hashNode())
	default:
		ensure(false, fmt.Sprintf("hashInternal: unexpected childrenCount=%d\n", n.childrenCount()))
		return []byte{}
	}
}
