package stateless

import (
	"bytes"
	"errors"
	"fmt"
)

// partialTrie is a standard-Ethereum MPT materialised only along the paths the
// proof provided. Missing children are hashNode references; resolving one means
// looking its hash up in the proof node map (nodes). A get/insert/delete that
// would need an unresolved boundary node returns errMissingNode — which means
// the proof was insufficient for the changeset (a generation bug, not a normal
// outcome).
type partialTrie struct {
	root  node
	nodes map[string][]byte // keccak(node) -> node RLP  (the multiproof)
}

var errMissingNode = errors.New("stateless: proof missing a node on a changed path")

// newPartialTrie loads proof nodes and roots the trie at rootHash.
// proofNodes is the flat set of RLP node blobs (order irrelevant); rootHash is
// the trusted pre-state root the proof must contain.
func newPartialTrie(rootHash []byte, proofNodes [][]byte) (*partialTrie, error) {
	t := &partialTrie{nodes: make(map[string][]byte, len(proofNodes))}
	for _, b := range proofNodes {
		t.nodes[string(keccak(b))] = append([]byte(nil), b...)
	}
	if len(rootHash) != 32 {
		return nil, fmt.Errorf("stateless: bad root hash len %d", len(rootHash))
	}
	// Empty trie: root is the well-known empty hash; nothing to resolve.
	if bytes.Equal(rootHash, emptyRootHash) {
		t.root = nil
		return t, nil
	}
	r, err := t.resolve(hashNode(rootHash))
	if err != nil {
		return nil, fmt.Errorf("stateless: resolve root: %w", err)
	}
	t.root = r
	return t, nil
}

// emptyRootHash = keccak(rlp("")) = keccak(0x80).
var emptyRootHash = keccak([]byte{0x80})

// resolve materialises a hashNode from the proof map. A non-hashNode is returned
// as-is. Returns errMissingNode if the hash isn't in the proof.
func (t *partialTrie) resolve(n node) (node, error) {
	h, ok := n.(hashNode)
	if !ok {
		return n, nil
	}
	blob, present := t.nodes[string(h)]
	if !present {
		return nil, errMissingNode
	}
	return decodeNode(h, blob)
}

// hash returns the current root hash, recomputing dirty nodes bottom-up.
func (t *partialTrie) hash() []byte {
	if t.root == nil {
		return append([]byte(nil), emptyRootHash...)
	}
	return nodeHash(t.root)
}

// get returns the value at hex key (with terminator), resolving boundary nodes
// as needed. found=false means proven-absent.
func (t *partialTrie) get(key []byte) (val []byte, found bool, err error) {
	return t.tget(t.root, key, 0)
}

func (t *partialTrie) tget(n node, key []byte, pos int) ([]byte, bool, error) {
	switch n := n.(type) {
	case nil:
		return nil, false, nil
	case valueNode:
		return n, true, nil
	case *shortNode:
		if len(key)-pos < len(n.key) || !bytes.Equal(n.key, key[pos:pos+len(n.key)]) {
			return nil, false, nil // diverges → absent
		}
		return t.tget(n.val, key, pos+len(n.key))
	case *fullNode:
		return t.tget(n.children[key[pos]], key, pos+1)
	case hashNode:
		rn, err := t.resolve(n)
		if err != nil {
			return nil, false, err
		}
		return t.tget(rn, key, pos)
	default:
		return nil, false, fmt.Errorf("stateless: get: bad node %T", n)
	}
}

// update sets (val!=nil) or deletes (val==nil) the leaf at hex key.
func (t *partialTrie) update(key, val []byte) error {
	if len(val) != 0 {
		_, nn, err := t.insert(t.root, nil, key, valueNode(val))
		if err != nil {
			return err
		}
		t.root = nn
		return nil
	}
	_, nn, err := t.delete(t.root, nil, key)
	if err != nil {
		return err
	}
	t.root = nn
	return nil
}

// insert returns (changed, newNode, err). prefix is the path consumed so far
// (for diagnostics only). Mirrors go-ethereum trie.insert, resolving hashNodes
// from the proof map.
func (t *partialTrie) insert(n node, prefix, key []byte, value node) (bool, node, error) {
	if len(key) == 0 {
		if v, ok := n.(valueNode); ok {
			return !bytes.Equal(v, value.(valueNode)), value, nil
		}
		return true, value, nil
	}
	switch n := n.(type) {
	case *shortNode:
		matchlen := prefixLen(key, n.key)
		// whole key matches → recurse into child
		if matchlen == len(n.key) {
			dirty, nn, err := t.insert(n.val, append(prefix, key[:matchlen]...), key[matchlen:], value)
			if err != nil {
				return false, nil, err
			}
			if !dirty {
				return false, n, nil
			}
			return true, &shortNode{key: n.key, val: nn, flags: dirtyFlag()}, nil
		}
		// split into a branch at divergence point
		branch := &fullNode{flags: dirtyFlag()}
		var err error
		_, branch.children[n.key[matchlen]], err = t.insert(nil, append(prefix, n.key[:matchlen+1]...), n.key[matchlen+1:], n.val)
		if err != nil {
			return false, nil, err
		}
		_, branch.children[key[matchlen]], err = t.insert(nil, append(prefix, key[:matchlen+1]...), key[matchlen+1:], value)
		if err != nil {
			return false, nil, err
		}
		if matchlen == 0 {
			return true, branch, nil
		}
		return true, &shortNode{key: key[:matchlen], val: branch, flags: dirtyFlag()}, nil
	case *fullNode:
		dirty, nn, err := t.insert(n.children[key[0]], append(prefix, key[0]), key[1:], value)
		if err != nil {
			return false, nil, err
		}
		if !dirty {
			return false, n, nil
		}
		c := n.copy()
		c.flags = dirtyFlag()
		c.children[key[0]] = nn
		return true, c, nil
	case nil:
		return true, &shortNode{key: key, val: value, flags: dirtyFlag()}, nil
	case hashNode:
		rn, err := t.resolve(n)
		if err != nil {
			return false, nil, err
		}
		dirty, nn, err := t.insert(rn, prefix, key, value)
		if err != nil {
			return false, nil, err
		}
		if !dirty {
			return false, rn, nil
		}
		return true, nn, nil
	default:
		return false, nil, fmt.Errorf("stateless: insert: bad node %T", n)
	}
}

// delete removes key. Mirrors go-ethereum trie.delete with boundary resolution.
func (t *partialTrie) delete(n node, prefix, key []byte) (bool, node, error) {
	switch n := n.(type) {
	case *shortNode:
		matchlen := prefixLen(key, n.key)
		if matchlen < len(n.key) {
			return false, n, nil // key not present → no change
		}
		if matchlen == len(key) {
			return true, nil, nil // delete the leaf/extension entirely
		}
		dirty, child, err := t.delete(n.val, append(prefix, key[:len(n.key)]...), key[len(n.key):])
		if err != nil {
			return false, nil, err
		}
		if !dirty {
			return false, n, nil
		}
		switch child := child.(type) {
		case *shortNode:
			// merge the two short nodes
			return true, &shortNode{key: concat(n.key, child.key...), val: child.val, flags: dirtyFlag()}, nil
		default:
			return true, &shortNode{key: n.key, val: child, flags: dirtyFlag()}, nil
		}
	case *fullNode:
		dirty, nn, err := t.delete(n.children[key[0]], append(prefix, key[0]), key[1:])
		if err != nil {
			return false, nil, err
		}
		if !dirty {
			return false, n, nil
		}
		c := n.copy()
		c.flags = dirtyFlag()
		c.children[key[0]] = nn

		// count remaining children; collapse a branch with a single child.
		pos := -1
		for i, cld := range &c.children {
			if cld != nil {
				if pos == -1 {
					pos = i
				} else {
					pos = -2
					break
				}
			}
		}
		if pos >= 0 {
			if pos != 16 {
				// resolve the surviving child so we can fold its key in
				cnode, err := t.resolveChildForCollapse(c.children[pos])
				if err != nil {
					return false, nil, err
				}
				if cn, ok := cnode.(*shortNode); ok {
					k := append([]byte{byte(pos)}, cn.key...)
					return true, &shortNode{key: k, val: cn.val, flags: dirtyFlag()}, nil
				}
			}
			// Surviving child is the value slot (pos==16) or a non-short node
			// (a branch / unresolved hash). Wrap in a ONE-nibble short node
			// keyed []byte{byte(pos)}: when pos==16 that nibble IS the 0x10 leaf
			// terminator (value-slot leaf); when pos<16 it is an extension into
			// the surviving branch. Appending an explicit 16 here was the bug —
			// it turned an extension into a bogus terminated leaf (go-ethereum
			// trie.delete uses []byte{byte(pos)}).
			return true, &shortNode{key: []byte{byte(pos)}, val: c.children[pos], flags: dirtyFlag()}, nil
		}
		return true, c, nil
	case valueNode:
		return true, nil, nil
	case nil:
		return false, nil, nil
	case hashNode:
		rn, err := t.resolve(n)
		if err != nil {
			return false, nil, err
		}
		dirty, nn, err := t.delete(rn, prefix, key)
		if err != nil {
			return false, nil, err
		}
		if !dirty {
			return false, rn, nil
		}
		return true, nn, nil
	default:
		return false, nil, fmt.Errorf("stateless: delete: bad node %T", n)
	}
}

// resolveChildForCollapse resolves a hashNode child encountered during a branch
// collapse so its (possibly short-node) shape can be merged.
func (t *partialTrie) resolveChildForCollapse(n node) (node, error) {
	if h, ok := n.(hashNode); ok {
		return t.resolve(h)
	}
	return n, nil
}

func dirtyFlag() nodeFlag { return nodeFlag{dirty: true} }

func concat(a []byte, b ...byte) []byte {
	out := make([]byte, len(a)+len(b))
	copy(out, a)
	copy(out[len(a):], b)
	return out
}
