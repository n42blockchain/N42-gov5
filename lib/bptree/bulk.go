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
	"sort"
)

// --- Upsert operations ---

func upsert(n *Node23, kvItems KeyValues, stats *Stats) (nodes []*Node23, newFirstKey *Felt, intermediateKeys []*Felt) {
	ensure(sort.IsSorted(kvItems), "kvItems are not sorted by key")

	if kvItems.Len() == 0 && n == nil {
		return []*Node23{n}, nil, []*Felt{}
	}
	if n == nil {
		n = makeEmptyLeafNode()
	}
	if n.isLeaf {
		return upsertLeaf(n, kvItems, stats)
	}
	return upsertInternal(n, kvItems, stats)
}

func upsertLeaf(n *Node23, kvItems KeyValues, stats *Stats) (nodes []*Node23, newFirstKey *Felt, intermediateKeys []*Felt) {
	ensure(n.isLeaf, "node is not leaf")

	if kvItems.Len() == 0 {
		if n.nextKey() != nil {
			intermediateKeys = append(intermediateKeys, n.nextKey())
		}
		return []*Node23{n}, nil, intermediateKeys
	}

	exposeNode(n, stats)

	currentFirstKey := n.firstKey()
	addOrReplaceLeaf(n, kvItems, stats)
	if n.firstKey() != currentFirstKey {
		newFirstKey = n.firstKey()
	}

	if n.keyCount() <= 3 {
		if n.nextKey() != nil {
			intermediateKeys = append(intermediateKeys, n.nextKey())
		}
		return []*Node23{n}, newFirstKey, intermediateKeys
	}

	for n.keyCount() > 3 {
		newLeaf := makeLeafNode(n.keys[:3], n.values[:3], stats)
		intermediateKeys = append(intermediateKeys, n.keys[2])
		nodes = append(nodes, newLeaf)
		n.keys, n.values = n.keys[2:], n.values[2:]
	}
	newLeaf := makeLeafNode(n.keys, n.values, stats)
	if n.nextKey() != nil {
		intermediateKeys = append(intermediateKeys, n.nextKey())
	}
	nodes = append(nodes, newLeaf)
	return nodes, newFirstKey, intermediateKeys
}

func upsertInternal(n *Node23, kvItems KeyValues, stats *Stats) (nodes []*Node23, newFirstKey *Felt, intermediateKeys []*Felt) {
	ensure(!n.isLeaf, "node is not internal")

	if kvItems.Len() == 0 {
		if n.lastLeaf().nextKey() != nil {
			intermediateKeys = append(intermediateKeys, n.lastLeaf().nextKey())
		}
		return []*Node23{n}, nil, intermediateKeys
	}

	exposeNode(n, stats)

	itemSubsets := splitItems(n, kvItems)

	newChildren := make([]*Node23, 0)
	newKeys := make([]*Felt, 0)
	for i := len(n.children) - 1; i >= 0; i-- {
		child := n.children[i]
		childNodes, childNewFirstKey, childIntermediateKeys := upsert(child, itemSubsets[i], stats)
		newChildren = append(childNodes, newChildren...)
		newKeys = append(childIntermediateKeys, newKeys...)
		if childNewFirstKey != nil {
			if i > 0 {
				propagateFirstKeyToPrevious(n.children[i-1], childNewFirstKey, stats)
			} else {
				newFirstKey = childNewFirstKey
			}
		}
	}

	n.children = newChildren
	if n.childrenCount() <= 3 {
		ensure(len(newKeys) > 0, "upsertInternal: newKeys count is zero")
		if len(newKeys) == len(n.children) {
			n.keys = newKeys[:len(newKeys)-1]
			intermediateKeys = append(intermediateKeys, newKeys[len(newKeys)-1])
		} else {
			n.keys = newKeys
		}
		// TODO(canepat): n.keys changed instead of making new node
		n.updated = true
		stats.UpdatedCount++
		return []*Node23{n}, newFirstKey, intermediateKeys
	}

	ensure(len(newKeys) >= n.childrenCount()-1 || n.childrenCount()%2 == 0 && n.childrenCount()%len(newKeys) == 0,
		"upsertInternal: inconsistent #children vs #newKeys")

	hasIntermediateKeys := len(newKeys) == n.childrenCount()-1 || len(newKeys) == n.childrenCount()

	for n.childrenCount() > 3 {
		nodes = append(nodes, makeInternalNode(n.children[:2], newKeys[:1], stats))
		n.children = n.children[2:]
		if hasIntermediateKeys {
			intermediateKeys = append(intermediateKeys, newKeys[1])
			newKeys = newKeys[2:]
		} else {
			newKeys = newKeys[1:]
		}
	}

	ensure(n.childrenCount() > 0 && len(newKeys) > 0, "upsertInternal: inconsistent #children vs #newKeys")
	switch n.childrenCount() {
	case 2:
		ensure(len(newKeys) > 0, "upsertInternal: inconsistent #newKeys")
		nodes = append(nodes, makeInternalNode(n.children, newKeys[:1], stats))
		intermediateKeys = append(intermediateKeys, newKeys[1:]...)
	case 3:
		ensure(len(newKeys) > 1, "upsertInternal: inconsistent #newKeys")
		nodes = append(nodes, makeInternalNode(n.children, newKeys[:2], stats))
		intermediateKeys = append(intermediateKeys, newKeys[2:]...)
	default:
		ensure(false, fmt.Sprintf("upsertInternal: inconsistent #children=%d #newKeys=%d\n", n.childrenCount(), len(newKeys)))
	}
	return nodes, newFirstKey, intermediateKeys
}

// --- Leaf add/replace ---

func addOrReplaceLeaf(n *Node23, kvItems KeyValues, stats *Stats) {
	ensure(n.isLeaf, "addOrReplaceLeaf: node is not leaf")
	ensure(len(n.keys) > 0 && len(n.values) > 0, "addOrReplaceLeaf: node keys/values are empty")
	ensure(len(kvItems.keys) > 0 && len(kvItems.keys) == len(kvItems.values), "addOrReplaceLeaf: invalid kvItems")

	nextKey, nextValue := n.nextKey(), n.nextValue()
	n.keys = n.keys[:len(n.keys)-1]
	n.values = n.values[:len(n.values)-1]

	switch n.keyCount() {
	case 0:
		n.keys = append(n.keys, kvItems.keys...)
		n.values = append(n.values, kvItems.values...)
	case 1:
		addOrReplaceLeaf1(n, kvItems, stats)
	case 2:
		addOrReplaceLeaf2(n, kvItems, stats)
	default:
		ensure(false, fmt.Sprintf("addOrReplaceLeaf: invalid key count %d", n.keyCount()))
	}

	n.keys = append(n.keys, nextKey)
	n.values = append(n.values, nextValue)
}

func addOrReplaceLeaf1(n *Node23, kvItems KeyValues, stats *Stats) {
	ensure(n.isLeaf, "addOrReplaceLeaf1: node is not leaf")
	ensure(n.keyCount() == 1, "addOrReplaceLeaf1: leaf has not 1 *canonical* key")

	key0, value0 := n.keys[0], n.values[0]
	index0 := sort.Search(kvItems.Len(), func(i int) bool { return *kvItems.keys[i] >= *key0 })

	if index0 >= kvItems.Len() {
		n.keys = append(kvItems.keys, key0)
		n.values = append(kvItems.values, value0)
		return
	}

	n.keys = append(make([]*Felt, 0), kvItems.keys[:index0]...)
	n.values = append(make([]*Felt, 0), kvItems.values[:index0]...)
	n.keys = append(n.keys, key0)
	n.values = append(n.values, value0)
	if *kvItems.keys[index0] == *key0 {
		n.keys = append(n.keys, kvItems.keys[index0+1:]...)
		n.values = append(n.values, kvItems.values[index0+1:]...)
		n.updated = true
		stats.UpdatedCount++
	} else {
		n.keys = append(n.keys, kvItems.keys[index0:]...)
		n.values = append(n.values, kvItems.values[index0:]...)
	}
}

func addOrReplaceLeaf2(n *Node23, kvItems KeyValues, stats *Stats) {
	ensure(n.isLeaf, "addOrReplaceLeaf2: node is not leaf")
	ensure(n.keyCount() == 2, "addOrReplaceLeaf2: leaf has not 2 *canonical* keys")

	key0, value0, key1, value1 := n.keys[0], n.values[0], n.keys[1], n.values[1]
	index0 := sort.Search(kvItems.Len(), func(i int) bool { return *kvItems.keys[i] >= *key0 })
	index1 := sort.Search(kvItems.Len(), func(i int) bool { return *kvItems.keys[i] >= *key1 })
	ensure(index1 >= index0, "addOrReplaceLeaf2: keys not ordered")

	if index0 >= kvItems.Len() {
		ensure(index1 == index0, "addOrReplaceLeaf2: keys not ordered")
		n.keys = append(kvItems.keys, key0, key1)
		n.values = append(kvItems.values, value0, value1)
		return
	}

	// Insert keys/values concatenating new ones around key0
	n.keys = append(make([]*Felt, 0), kvItems.keys[:index0]...)
	n.values = append(make([]*Felt, 0), kvItems.values[:index0]...)
	n.keys = append(n.keys, key0)
	n.values = append(n.values, value0)

	if *kvItems.keys[index0] == *key0 {
		n.updated = true
		stats.UpdatedCount++
		if index1 < kvItems.Len() {
			n.keys = append(n.keys, kvItems.keys[index0+1:index1]...)
			n.values = append(n.values, kvItems.values[index0+1:index1]...)
		} else {
			n.keys = append(n.keys, kvItems.keys[index0+1:]...)
			n.values = append(n.values, kvItems.values[index0+1:]...)
			n.keys = append(n.keys, key1)
			n.values = append(n.values, value1)
			return
		}
	} else {
		if index1 < kvItems.Len() {
			n.keys = append(n.keys, kvItems.keys[index0:index1]...)
			n.values = append(n.values, kvItems.values[index0:index1]...)
		} else {
			n.keys = append(n.keys, kvItems.keys[index0:]...)
			n.values = append(n.values, kvItems.values[index0:]...)
			n.keys = append(n.keys, key1)
			n.values = append(n.values, value1)
			return
		}
	}

	// Insert key1
	n.keys = append(n.keys, key1)
	n.values = append(n.values, value1)
	if *kvItems.keys[index1] == *key1 {
		n.keys = append(n.keys, kvItems.keys[index1+1:]...)
		n.values = append(n.values, kvItems.values[index1+1:]...)
		if !n.updated {
			n.updated = true
			stats.UpdatedCount++
		}
	} else {
		n.keys = append(n.keys, kvItems.keys[index1:]...)
		n.values = append(n.values, kvItems.values[index1:]...)
	}
}

func splitItems(n *Node23, kvItems KeyValues) []KeyValues {
	ensure(!n.isLeaf, "splitItems: node is not internal")
	ensure(len(n.keys) > 0, "splitItems: internal node has no keys")

	itemSubsets := make([]KeyValues, 0)
	for i, key := range n.keys {
		splitIndex := sort.Search(kvItems.Len(), func(i int) bool { return *kvItems.keys[i] >= *key })
		itemSubsets = append(itemSubsets, KeyValues{kvItems.keys[:splitIndex], kvItems.values[:splitIndex]})
		kvItems = KeyValues{kvItems.keys[splitIndex:], kvItems.values[splitIndex:]}
		if i == len(n.keys)-1 {
			itemSubsets = append(itemSubsets, kvItems)
		}
	}
	ensure(len(itemSubsets) == len(n.children), "item subsets and children have different cardinality")
	return itemSubsets
}

// --- Delete operations ---

func del(n *Node23, keysToDelete []Felt, stats *Stats) (deleted *Node23, nextKey *Felt, intermediateKeys []*Felt) {
	ensure(sort.IsSorted(Keys(keysToDelete)), "keysToDelete are not sorted")

	if n == nil {
		return n, nil, intermediateKeys
	}
	if n.isLeaf {
		return deleteLeaf(n, keysToDelete, stats)
	}
	return deleteInternal(n, keysToDelete, stats)
}

func deleteLeaf(n *Node23, keysToDelete []Felt, stats *Stats) (deleted *Node23, nextKey *Felt, intermediateKeys []*Felt) {
	ensure(n.isLeaf, fmt.Sprintf("node %s is not leaf", n))

	if len(keysToDelete) == 0 {
		if n.nextKey() != nil {
			intermediateKeys = append(intermediateKeys, n.nextKey())
		}
		return n, nil, intermediateKeys
	}

	exposeNode(n, stats)

	currentFirstKey := n.firstKey()
	deleteLeafKeys(n, keysToDelete, stats)

	if n.keyCount() == 1 {
		return nil, n.nextKey(), intermediateKeys
	}

	if n.nextKey() != nil {
		intermediateKeys = append(intermediateKeys, n.nextKey())
	}
	if n.firstKey() != currentFirstKey {
		return n, n.firstKey(), intermediateKeys
	}
	return n, nil, intermediateKeys
}

func deleteLeafKeys(n *Node23, keysToDelete []Felt, stats *Stats) (deleted KeyValues) {
	ensure(n.isLeaf, "deleteLeafKeys: node is not leaf")
	switch n.keyCount() {
	case 2:
		if Keys(keysToDelete).Contains(*n.keys[0]) {
			deleted.keys = n.keys[:1]
			deleted.values = n.values[:1]
			n.keys = n.keys[1:]
			n.values = n.values[1:]
			stats.DeletedCount++
		}
	case 3:
		key0Match := Keys(keysToDelete).Contains(*n.keys[0])
		key1Match := Keys(keysToDelete).Contains(*n.keys[1])
		switch {
		case key0Match && key1Match:
			deleted.keys = n.keys[:2]
			deleted.values = n.values[:2]
			n.keys = n.keys[2:]
			n.values = n.values[2:]
			stats.DeletedCount++
		case key0Match:
			deleted.keys = n.keys[:1]
			deleted.values = n.values[:1]
			n.keys = n.keys[1:]
			n.values = n.values[1:]
			n.updated = true
			stats.UpdatedCount++
		case key1Match:
			deleted.keys = n.keys[1:2]
			deleted.values = n.values[1:2]
			n.keys = append(n.keys[:1], n.keys[2])
			n.values = append(n.values[:1], n.values[2])
			n.updated = true
			stats.UpdatedCount++
		}
	default:
		ensure(false, fmt.Sprintf("unexpected number of keys in %s", n))
	}
	return deleted
}

func deleteInternal(n *Node23, keysToDelete []Felt, stats *Stats) (deleted *Node23, nextKey *Felt, intermediateKeys []*Felt) {
	ensure(!n.isLeaf, fmt.Sprintf("node %s is not internal", n))

	if len(keysToDelete) == 0 {
		if n.lastLeaf().nextKey() != nil {
			intermediateKeys = append(intermediateKeys, n.lastLeaf().nextKey())
		}
		return n, nil, intermediateKeys
	}

	exposeNode(n, stats)

	keySubsets := splitKeys(n, keysToDelete)

	newKeys := make([]*Felt, 0)
	for i := len(n.children) - 1; i >= 0; i-- {
		child, childNextKey, childIntermediateKeys := del(n.children[i], keySubsets[i], stats)
		newKeys = append(childIntermediateKeys, newKeys...)
		if i > 0 {
			previousIndex := i - 1
			previousChild := n.children[previousIndex]
			for previousChild.isEmpty() && previousIndex-1 >= 0 {
				previousChild = n.children[previousIndex-1]
				previousIndex--
			}
			if child == nil || childNextKey != nil {
				propagateNextKeyToPrevious(previousChild, childNextKey, stats)
			}
			if !previousChild.isEmpty() && child != nil && child.childrenCount() == 1 {
				child.keys = child.keys[:0]
				newLeft, newRight := mergeRight2Left(previousChild, child, stats)
				n.children = append(n.children[:previousIndex], append([]*Node23{newLeft, newRight}, n.children[i+1:]...)...)
			}
		} else {
			nextIndex := i + 1
			nextChild := n.children[nextIndex]
			for nextChild.isEmpty() && nextIndex+1 < n.childrenCount() {
				nextChild = n.children[nextIndex+1]
				nextIndex++
			}
			if !nextChild.isEmpty() && child != nil && child.childrenCount() == 1 {
				child.keys = child.keys[:0]
				newLeft, newRight := mergeLeft2Right(child, nextChild, stats)
				n.children = append([]*Node23{newLeft, newRight}, n.children[nextIndex+1:]...)
			}
			if childNextKey != nil {
				nextKey = childNextKey
			}
		}
	}

	switch len(n.children) {
	case 2:
		nextKey, intermediateKeys = update2Node(n, newKeys, nextKey, intermediateKeys, stats)
	case 3:
		nextKey, intermediateKeys = update3Node(n, newKeys, nextKey, intermediateKeys, stats)
	default:
		ensure(false, fmt.Sprintf("unexpected number of children in %s", n))
	}

	for _, child := range n.children {
		if child.updated {
			n.updated = true
			stats.UpdatedCount++
			break
		}
	}

	if n.keyCount() == 0 {
		return nil, nextKey, intermediateKeys
	}
	return n, nextKey, intermediateKeys
}

// --- Merge operations ---

func mergeLeft2Right(left, right *Node23, stats *Stats) (newLeft, newRight *Node23) {
	ensure(!left.isLeaf, "mergeLeft2Right: left is leaf")
	ensure(left.childrenCount() > 0, "mergeLeft2Right: left has no children")

	if left.firstChild().childrenCount() == 1 {
		newLeftFirstChild, newRightFirstChild := mergeLeft2Right(left.firstChild(), right.firstChild(), stats)
		left = makeInternalNode([]*Node23{newLeftFirstChild}, left.keys, stats)
		right = makeInternalNode(append([]*Node23{newRightFirstChild}, right.children[1:]...), right.keys, stats)
	}

	if right.childrenCount() >= 3 {
		return mergeRight2Left(left, right, stats)
	}
	if left.firstChild().isEmpty() {
		return makeInternalNode([]*Node23{}, []*Felt{}, stats), right
	}
	if right.childrenCount() == 1 {
		if right.firstChild().isEmpty() {
			return left, makeInternalNode([]*Node23{}, []*Felt{}, stats)
		}
		newRight = makeInternalNode(
			append([]*Node23{left.firstChild()}, right.children...),
			[]*Felt{left.lastLeaf().nextKey()},
			stats,
		)
		if left.keyCount() > 1 {
			newLeft = makeInternalNode(left.children[1:], left.keys[1:], stats)
		} else {
			newLeft = makeInternalNode(left.children[1:], left.keys, stats)
		}
		return newLeft, newRight
	}

	newRight = makeInternalNode(
		append([]*Node23{left.firstChild()}, right.children...),
		append([]*Felt{left.lastLeaf().nextKey()}, right.keys...),
		stats,
	)
	if left.keyCount() > 1 {
		newLeft = makeInternalNode(left.children[1:], left.keys[1:], stats)
	} else {
		newLeft = makeInternalNode(left.children[1:], left.keys, stats)
	}
	return newLeft, newRight
}

func mergeRight2Left(left, right *Node23, stats *Stats) (newLeft, newRight *Node23) {
	ensure(!right.isLeaf, "mergeRight2Left: right is leaf")
	ensure(right.childrenCount() > 0, "mergeRight2Left: right has no children")

	if right.firstChild().childrenCount() == 1 {
		newLeftLastChild, newRightFirstChild := mergeRight2Left(left.lastChild(), right.firstChild(), stats)
		left = makeInternalNode(append(left.children[:len(left.children)-1], newLeftLastChild), left.keys, stats)
		right = makeInternalNode([]*Node23{newRightFirstChild}, right.keys, stats)
	}

	if left.childrenCount() >= 3 {
		return mergeLeft2Right(left, right, stats)
	}

	if right.firstChild().isEmpty() {
		return left, makeInternalNode([]*Node23{}, []*Felt{}, stats)
	}

	if left.childrenCount() == 1 && left.firstChild().isEmpty() {
		return makeInternalNode([]*Node23{}, []*Felt{}, stats), right
	}

	if left.childrenCount() == 1 {
		newLeft = makeInternalNode(
			append(left.children, right.firstChild()),
			[]*Felt{right.firstLeaf().firstKey()},
			stats,
		)
	} else {
		newLeft = makeInternalNode(
			append(left.children, right.firstChild()),
			append(left.keys, right.firstLeaf().firstKey()),
			stats,
		)
	}
	if right.keyCount() > 1 {
		newRight = makeInternalNode(right.children[1:], right.keys[1:], stats)
	} else {
		newRight = makeInternalNode(right.children[1:], right.keys, stats)
	}
	return newLeft, newRight
}

// --- Key splitting ---

func splitKeys(n *Node23, keysToDelete []Felt) [][]Felt {
	ensure(!n.isLeaf, "splitKeys: node is not internal")
	ensure(len(n.keys) > 0, fmt.Sprintf("splitKeys: internal node %s has no keys", n))

	keySubsets := make([][]Felt, 0)
	for i, key := range n.keys {
		splitIndex := sort.Search(len(keysToDelete), func(i int) bool { return keysToDelete[i] >= *key })
		keySubsets = append(keySubsets, keysToDelete[:splitIndex])
		keysToDelete = keysToDelete[splitIndex:]
		if i == len(n.keys)-1 {
			keySubsets = append(keySubsets, keysToDelete)
		}
	}
	ensure(len(keySubsets) == len(n.children), "key subsets and children have different cardinality")
	return keySubsets
}

// --- Node update helpers ---

func update2Node(n *Node23, newKeys []*Felt, nextKey *Felt, intermediateKeys []*Felt, stats *Stats) (*Felt, []*Felt) {
	ensure(len(n.children) == 2, "update2Node: wrong number of children")

	switch len(newKeys) {
	case 0:
	case 1:
		n.keys = newKeys
	case 2:
		n.keys = newKeys[:1]
		intermediateKeys = append(intermediateKeys, newKeys[1])
	default:
		ensure(false, fmt.Sprintf("update2Node: wrong number of newKeys=%d", len(newKeys)))
	}

	nodeA, nodeC := n.children[0], n.children[1]
	if nodeA.isEmpty() {
		if nodeC.isEmpty() {
			n.children = n.children[:0]
			n.keys = n.keys[:0]
			if nodeC.isLeaf {
				return nodeC.nextKey(), intermediateKeys
			}
			return nextKey, intermediateKeys
		}
		n.children = n.children[1:]
		if nodeA.isLeaf {
			return nodeA.nextKey(), intermediateKeys
		}
		return nextKey, intermediateKeys
	}
	if nodeC.isEmpty() {
		n.children = n.children[:1]
		if nodeC.isLeaf {
			nodeA.setNextKey(nodeC.nextKey(), stats)
		}
		return nextKey, intermediateKeys
	}
	n.keys = []*Felt{nodeA.lastLeaf().nextKey()}
	return nextKey, intermediateKeys
}

func update3Node(n *Node23, newKeys []*Felt, nextKey *Felt, intermediateKeys []*Felt, stats *Stats) (*Felt, []*Felt) {
	ensure(len(n.children) == 3, "update3Node: wrong number of children")

	switch len(newKeys) {
	case 0:
	case 1, 2:
		n.keys = newKeys
	case 3:
		n.keys = newKeys[:2]
		intermediateKeys = append(intermediateKeys, newKeys[2])
	default:
		ensure(false, fmt.Sprintf("update3Node: wrong number of newKeys=%d", len(newKeys)))
	}

	nodeA, nodeB, nodeC := n.children[0], n.children[1], n.children[2]
	aEmpty, bEmpty, cEmpty := nodeA.isEmpty(), nodeB.isEmpty(), nodeC.isEmpty()

	if aEmpty {
		if bEmpty {
			if cEmpty {
				n.children = n.children[:0]
				n.keys = n.keys[:0]
				if nodeA.isLeaf {
					return nodeC.nextKey(), intermediateKeys
				}
				return nextKey, intermediateKeys
			}
			n.children = n.children[2:]
			if nodeA.isLeaf {
				return nodeB.nextKey(), intermediateKeys
			}
			return nextKey, intermediateKeys
		}
		if cEmpty {
			n.children = n.children[1:2]
			if nodeA.isLeaf {
				nodeB.setNextKey(nodeC.nextKey(), stats)
				return nodeA.nextKey(), intermediateKeys
			}
			return nextKey, intermediateKeys
		}
		n.children = n.children[1:]
		if nodeA.isLeaf {
			n.keys = []*Felt{nodeB.nextKey()}
			return nodeA.nextKey(), intermediateKeys
		}
		n.keys = []*Felt{nodeB.lastLeaf().nextKey()}
		return nextKey, intermediateKeys
	}

	if bEmpty {
		if cEmpty {
			n.children = n.children[:1]
			if nodeA.isLeaf {
				nodeA.setNextKey(nodeC.nextKey(), stats)
			}
			return nextKey, intermediateKeys
		}
		n.children = append(n.children[:1], n.children[2])
		if nodeA.isLeaf {
			n.keys = []*Felt{nodeB.nextKey()}
			nodeA.setNextKey(nodeB.nextKey(), stats)
		} else {
			n.keys = []*Felt{nodeA.lastLeaf().nextKey()}
		}
		return nextKey, intermediateKeys
	}

	if cEmpty {
		n.children = n.children[:2]
		if nodeA.isLeaf {
			n.keys = []*Felt{nodeA.nextKey()}
			nodeB.setNextKey(nodeC.nextKey(), stats)
		} else {
			n.keys = []*Felt{nodeA.lastLeaf().nextKey()}
		}
		return nextKey, intermediateKeys
	}

	return nextKey, intermediateKeys
}

// --- Demote ---

func demote(node *Node23, nextKey *Felt, intermediateKeys []*Felt, stats *Stats) (*Node23, *Felt) {
	if node == nil {
		return nil, nextKey
	}

	switch len(node.children) {
	case 0:
		if len(node.keys) == 0 {
			return nil, nextKey
		}
		return node, nextKey
	case 1:
		return demote(node.children[0], nextKey, intermediateKeys, stats)
	case 2:
		firstChild, secondChild := node.children[0], node.children[1]
		if firstChild.keyCount() == 0 && secondChild.keyCount() == 0 {
			return nil, nextKey
		}
		if firstChild.keyCount() == 0 && secondChild.keyCount() > 0 {
			return secondChild, nextKey
		}
		if firstChild.keyCount() > 0 && secondChild.keyCount() == 0 {
			return firstChild, nextKey
		}
		if firstChild.keyCount() == 2 && secondChild.keyCount() == 2 && firstChild.isLeaf {
			keys := []*Felt{firstChild.firstKey(), secondChild.firstKey(), secondChild.nextKey()}
			values := []*Felt{firstChild.firstValue(), secondChild.firstValue(), secondChild.nextValue()}
			return makeLeafNode(keys, values, stats), nextKey
		}
	}
	return node, nextKey
}

// --- Shared helpers ---

func exposeNode(n *Node23, stats *Stats) {
	if !n.exposed {
		n.exposed = true
		stats.ExposedCount++
		stats.OpeningHashes += n.howManyHashes()
	}
}

func propagateFirstKeyToPrevious(previousChild *Node23, newFirstKey *Felt, stats *Stats) {
	if previousChild.isLeaf {
		ensure(len(previousChild.keys) > 0, "upsertInternal: previousChild has no keys")
		if previousChild.nextKey() != newFirstKey {
			previousChild.setNextKey(newFirstKey, stats)
		}
	} else {
		ensure(len(previousChild.children) > 0, "upsertInternal: previousChild has no children")
		lastLeaf := previousChild.lastLeaf()
		if lastLeaf.nextKey() != newFirstKey {
			lastLeaf.setNextKey(newFirstKey, stats)
		}
	}
	// TODO(canepat): previousChild/previousLastLeaf changed instead of making new node
}

func propagateNextKeyToPrevious(previousChild *Node23, childNextKey *Felt, stats *Stats) {
	if previousChild.isLeaf {
		ensure(len(previousChild.keys) > 0, "delete: previousChild has no keys")
		if previousChild.nextKey() != childNextKey {
			previousChild.setNextKey(childNextKey, stats)
		}
	} else {
		ensure(len(previousChild.children) > 0, "delete: previousChild has no children")
		lastLeaf := previousChild.lastLeaf()
		if lastLeaf.nextKey() != childNextKey {
			lastLeaf.setNextKey(childNextKey, stats)
		}
	}
}
