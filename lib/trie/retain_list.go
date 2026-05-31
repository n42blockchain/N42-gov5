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

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/common/account"
	"github.com/holiman/uint256"
)

type RetainDecider interface {
	Retain([]byte) bool
	IsCodeTouched(types.Hash) bool
}

type RetainDeciderWithMarker interface {
	RetainDecider
	// AddKeyWithMarker adds a key in KEY encoding with marker and returns the
	// nibble encoded key.
	AddKeyWithMarker(key []byte, marker bool) []byte
	RetainWithMarker(prefix []byte) (retain bool, nextMarkedKey []byte)
}

// ProofRetainer is a wrapper around the RetainList passed to the trie builder.
// It is responsible for aggregating proof values from the trie computation and
// will return a valid account.AccProofresult after the trie root hash
// calculation has completed.
type ProofRetainer struct {
	rl             *RetainList
	addr           types.Address
	acc            *account.StateAccount
	accHexKey      []byte
	storageKeys    []types.Hash
	storageHexKeys [][]byte
	proofs         []*proofElement
}

// NewProofRetainer creates a new ProofRetainer instance for a given account and
// set of storage keys.  The trie keys corresponding to the account key, and its
// storage keys are added to the given RetainList.  The ProofRetainer should be
// set onto the FlatDBTrieLoader via SetProofRetainer before performing its Load
// operation in order to appropriately collect the proof elements.
func NewProofRetainer(addr types.Address, a *account.StateAccount, storageKeys []types.Hash, rl *RetainList) (*ProofRetainer, error) {
	addrHash, err := types.HashData(addr[:])
	if err != nil {
		return nil, err
	}
	accHexKey := rl.AddKey(addrHash[:])

	storageHexKeys := make([][]byte, len(storageKeys))
	for i, sk := range storageKeys {
		storageHash, err := types.HashData(sk[:])
		if err != nil {
			return nil, err
		}

		// Incarnation removed: N42's storage trie key is addrHash || slotHash
		// (no 8-byte incarnation — see trie_root.go genStructStorage, which
		// builds the proof element key as a 64-nibble fullKey). The retain key
		// MUST match that layout, otherwise the storage walk prefix diverges at
		// nibble 64 (incarnation zeros vs. slot nibbles) and no storage proof
		// element is ever collected.
		var compactEncoded [2 * length.Hash]byte
		copy(compactEncoded[:length.Hash], addrHash[:])
		copy(compactEncoded[length.Hash:], storageHash[:])
		storageHexKeys[i] = rl.AddKey(compactEncoded[:])
	}

	return &ProofRetainer{
		rl:             rl,
		addr:           addr,
		acc:            a,
		accHexKey:      accHexKey,
		storageKeys:    storageKeys,
		storageHexKeys: storageHexKeys,
	}, nil
}

// ProofElement requests a new proof element for a given prefix.  This proof
// element is retained by the ProofRetainer, and will be utilized to compute the
// proof after the trie computation has completed.  The prefix is the standard
// nibble encoded prefix used in the rest of the trie computations.
func (pr *ProofRetainer) ProofElement(prefix []byte) *proofElement {
	if !pr.rl.Retain(prefix) {
		return nil
	}

	switch {
	case bytes.HasPrefix(pr.accHexKey, prefix):
		// This prefix is a node between the account and the root
	case bytes.HasPrefix(prefix, pr.accHexKey):
		// This prefix is the account or one of its storage nodes
	default:
		// we do not need a proof element for this prefix
		return nil
	}

	pe := &proofElement{
		hexKey: append([]byte{}, prefix...),
	}
	// Since we do a depth-first traversal, reverse the proof elements so that
	// they are ordered correctly root -> node -> ... -> leaf as dictated by
	// EIP-1186
	pr.proofs = append([]*proofElement{pe}, pr.proofs...)
	return pe
}

// ProofResult may be invoked only after the Load function of the
// FlatDBTrieLoader has successfully executed.  It will populate the Address,
// Balance, Nonce, and CodeHash from the account data supplied in the
// constructor, the StorageHash, storageKey values, and proof elements are
// supplied by the Load operation of the trie construction.
func (pr *ProofRetainer) ProofResult() (*account.AccProofResult, error) {
	result := &account.AccProofResult{
		Address:  pr.addr,
		Balance:  pr.acc.Balance.ToBig().String(),
		Nonce:    pr.acc.Nonce,
		CodeHash: pr.acc.CodeHash,
	}

	for _, pe := range pr.proofs {
		if !bytes.HasPrefix(pr.accHexKey, pe.hexKey) {
			continue
		}
		result.AccountProof = append(result.AccountProof, pe.proof.Bytes())
		if bytes.Equal(pr.accHexKey, pe.storageRootKey) {
			result.StorageHash = pe.storageRoot
		}
	}

	if pr.acc.Initialised && result.StorageHash == (types.Hash{}) {
		return nil, fmt.Errorf("did not find storage root in proof elements")
	}

	result.StorageProof = make([]account.StorProofResult, len(pr.storageKeys))
	for i, sk := range pr.storageKeys {
		result.StorageProof[i].Key = sk
		hexKey := pr.storageHexKeys[i]
		if !pr.acc.Initialised || result.StorageHash == EmptyRoot {
			// The yellow paper makes it clear that the EmptyRoot is a special case
			// when the trie has no nodes, but EIP-1186 states that the proof is
			// "starting with the storageHash-Node".  Since the trie has no nodes,
			// it's unclear whether the correct proof should contain the EmptyRoot
			// pre-image of RLP([]byte(nil)), or be empty.  This implementation
			// chooses 'empty' as it seems more consistent and it is expected that
			// provers will treat the EmptyRoot as a special case and ignore the proof
			// bytes.
			result.StorageProof[i].Value = "0"
			continue
		}

		for _, pe := range pr.proofs {
			// Account-path interior nodes are strictly shorter than the 64-nibble
			// account key; skip them. (Incarnation removed: storage-subtree nodes
			// start at the account key length, so we can no longer rely on the
			// old 16-nibble incarnation gap to separate them by length alone.)
			if len(pe.hexKey) < 2*length.Hash {
				continue
			}
			// The account leaf sits at exactly the account key length and records
			// the storageRoot; it belongs to the account proof, not the storage
			// subtree. The storage-subtree ROOT also sits at that length (empty
			// storage prefix) but never sets storageRootKey, so this cleanly keeps
			// the storage root while excluding the account leaf.
			if len(pe.storageRootKey) > 0 {
				continue
			}
			if !bytes.HasPrefix(hexKey, pe.hexKey) {
				continue
			}

			if pe.storageValue != nil && bytes.Equal(pe.storageKey, hexKey[2*length.Hash:]) {
				result.StorageProof[i].Value = pe.storageValue.ToBig().String()
			}

			result.StorageProof[i].Proof = append(result.StorageProof[i].Proof, pe.proof.Bytes())
		}

		if result.StorageProof[i].Value == "" {
			result.StorageProof[i].Value = "0"
		}
	}

	return result, nil
}

// proofElement represent a node or leaf in the trie and its
// corresponding RLP encoding.  We store the elements individually when
// aggregating as multiple keys (in particular storage keys) may need to
// reference the same proof elements in their Merkle proof.
type proofElement struct {
	// key is the hex encoded key indicating the path of the
	// element in the proof.
	hexKey []byte

	// buf is used to store the proof bytes
	proof bytes.Buffer

	// storageRoot stores the storage root if this is writing
	// an account leaf
	storageRoot types.Hash

	// storageRootKey stores the actual hexKey from which the storageRoot came.
	// This is needed because other proof nodes can be included in the negative
	// proof case.
	storageRootKey []byte

	// storageValue stores the value of the particular storage key if this writer
	// is for a storage key
	storageValue *uint256.Int

	// storageKey stores the actual hexKey from which the storageValue came. This
	// is needed because the same proofElement may be used to both prove and
	// disprove two different storage elements.
	storageKey []byte
}

// proofCollector receives a node-proof request for a trie prefix during root
// computation; returning non-nil captures that node's RLP into the element.
// ProofRetainer (single account, EIP-1186 AccProofResult) and WitnessRetainer
// (multi-key flat block witness) both implement it. FlatDBTrieLoader holds one
// and invokes ProofElement as each trie node is emitted.
type proofCollector interface {
	ProofElement(prefix []byte) *proofElement
}

// WitnessRetainer collects every proof node on the paths of a set of
// already-hashed keys into a flat, de-duplicated node set — a block witness
// sufficient for stateless re-execution INCLUDING the sibling subtrees a delete
// needs when its branch collapses (add the deleted key's trie neighbours so
// their paths reveal the surviving sibling). Unlike ProofRetainer it neither
// hashes inputs nor builds an AccProofResult: callers add hashed account keys
// (32B) and/or hashed storage composite keys (64B = addrHash||slotHash) via
// AddHashedKey and read the merged RLP nodes via Nodes().
type WitnessRetainer struct {
	rl    *RetainList
	keys  [][]byte // nibble-encoded retained keys, for prefix relevance tests
	elems []*proofElement

	// relevance index, built lazily on first ProofElement (keys must be final by
	// then — the loader call). Replaces an O(len(keys)) linear scan per visited
	// node, which is O(nodes×keys) overall and stalls on high-churn windows (e.g.
	// the 2016 DoS-spam blocks touch 100k+ keys → 10^11 ops). sorted enables an
	// O(log keys) "prefix is a prefix of some key" test; set enables an O(pathlen)
	// "some key is a prefix of prefix" test.
	prepared bool
	sorted   [][]byte
	set      map[string]struct{}
}

func NewWitnessRetainer(rl *RetainList) *WitnessRetainer {
	return &WitnessRetainer{rl: rl}
}

// NewWitnessRetainerFromList builds a WitnessRetainer over the keys ALREADY in
// rl (e.g. the dirty-key RetainList a root computation runs over). The same rl
// serves as both the RetainDecider for the loader and the relevance source, so
// attaching the returned retainer to that computation captures exactly the
// touched-path multiproof — no separate AddHashedKey calls needed.
func NewWitnessRetainerFromList(rl *RetainList) *WitnessRetainer {
	w := &WitnessRetainer{rl: rl}
	w.keys = make([][]byte, len(rl.hexes))
	for i, k := range rl.hexes {
		w.keys[i] = append([]byte(nil), k...)
	}
	return w
}

// AddHashedKey registers an already-hashed key (raw bytes) to be proven.
func (w *WitnessRetainer) AddHashedKey(key []byte) {
	w.keys = append(w.keys, w.rl.AddKey(key))
}

// prepareIndex builds the sorted key list + key set used by ProofElement's
// relevance test. Called once on the first ProofElement; keys must be final.
func (w *WitnessRetainer) prepareIndex() {
	w.sorted = make([][]byte, len(w.keys))
	copy(w.sorted, w.keys)
	sort.Slice(w.sorted, func(i, j int) bool { return bytes.Compare(w.sorted[i], w.sorted[j]) < 0 })
	w.set = make(map[string]struct{}, len(w.keys))
	for _, k := range w.keys {
		w.set[string(k)] = struct{}{}
	}
	w.prepared = true
}

// relevant reports whether prefix lies on the path to a registered key (some key
// has prefix as a prefix) or under one (some key is a prefix of prefix) — the
// same predicate as the old linear scan, but O(log keys + len(prefix)) instead of
// O(len(keys)). The path case is the common one (multiproof); the under case
// covers the delete-collapse sibling subtrees the transition tool needs.
func (w *WitnessRetainer) relevant(prefix []byte) bool {
	// path case: first sorted key >= prefix; if it starts with prefix, a key lies
	// on/under this node's path. (Any key with prefix as a prefix is >= prefix and
	// is the first such; the immediate successor of prefix is the candidate.)
	i := sort.Search(len(w.sorted), func(i int) bool { return bytes.Compare(w.sorted[i], prefix) >= 0 })
	if i < len(w.sorted) && bytes.HasPrefix(w.sorted[i], prefix) {
		return true
	}
	// under case: some key equals a proper prefix of prefix (prefix is deeper than
	// a registered full key — e.g. a leaf's value node). Bounded by len(prefix).
	for j := 1; j <= len(prefix); j++ {
		if _, ok := w.set[string(prefix[:j])]; ok {
			return true
		}
	}
	return false
}

// ProofElement captures the node at prefix when that prefix lies on the path to,
// or under, any registered key.
func (w *WitnessRetainer) ProofElement(prefix []byte) *proofElement {
	if !w.rl.Retain(prefix) {
		return nil
	}
	if !w.prepared {
		w.prepareIndex()
	}
	if !w.relevant(prefix) {
		return nil
	}
	pe := &proofElement{hexKey: append([]byte{}, prefix...)}
	w.elems = append(w.elems, pe)
	return pe
}

// Nodes returns the collected proof nodes (RLP), de-duplicated by content.
func (w *WitnessRetainer) Nodes() [][]byte {
	seen := make(map[string]struct{}, len(w.elems))
	out := make([][]byte, 0, len(w.elems))
	for _, pe := range w.elems {
		b := pe.proof.Bytes()
		if len(b) == 0 {
			continue
		}
		if _, ok := seen[string(b)]; ok {
			continue
		}
		seen[string(b)] = struct{}{}
		out = append(out, append([]byte(nil), b...))
	}
	return out
}

// RetainList encapsulates the list of keys that are required to be fully available, or loaded
// (by using `BRANCH` opcode instead of `HASHER`) after processing of the sequence of key-value
// pairs
// DESCRIBED: docs/programmers_guide/guide.md#converting-sequence-of-keys-and-value-into-a-multiproof
type RetainList struct {
	inited      bool // Whether keys are sorted and "LTE" and "GT" indices set
	minLength   int  // Mininum length of prefixes for which `HashOnly` function can return `true`
	lteIndex    int  // Index of the "LTE" key in the keys slice. Next one is "GT"
	hexes       [][]byte
	markers     []bool
	codeTouches map[types.Hash]struct{}
}

// NewRetainList creates new RetainList
func NewRetainList(minLength int) *RetainList {
	return &RetainList{minLength: minLength, codeTouches: make(map[types.Hash]struct{})}
}

func (rl *RetainList) Len() int {
	return len(rl.hexes)
}
func (rl *RetainList) Less(i, j int) bool {
	return bytes.Compare(rl.hexes[i], rl.hexes[j]) < 0
}
func (rl *RetainList) Swap(i, j int) {
	rl.hexes[i], rl.hexes[j] = rl.hexes[j], rl.hexes[i]
	rl.markers[i], rl.markers[j] = rl.markers[j], rl.markers[i]
}

// AddKey adds a new key (in KEY encoding) to the list
func (rl *RetainList) AddKey(key []byte) []byte {
	return rl.AddKeyWithMarker(key, false)
}

func (rl *RetainList) AddKeyWithMarker(key []byte, marker bool) []byte {
	var nibbles = make([]byte, 2*len(key))
	for i, b := range key {
		nibbles[i*2] = b / 16
		nibbles[i*2+1] = b % 16
	}
	rl.AddHex(nibbles)
	rl.markers = append(rl.markers, marker)
	return nibbles
}

// AddHex adds a new key (in HEX encoding) to the list
func (rl *RetainList) AddHex(hex []byte) {
	rl.hexes = append(rl.hexes, hex)
}

// AddCodeTouch adds a new code touch into the resolve set
func (rl *RetainList) AddCodeTouch(codeHash types.Hash) {
	rl.codeTouches[codeHash] = struct{}{}
}

func (rl *RetainList) IsCodeTouched(codeHash types.Hash) bool {
	_, ok := rl.codeTouches[codeHash]
	return ok
}

func (rl *RetainList) ensureInited() {
	if rl.inited {
		return
	}
	if len(rl.markers) == 0 {
		rl.markers = make([]bool, len(rl.hexes))
	}
	if !sort.IsSorted(rl) {
		sort.Sort(rl)
	}
	rl.lteIndex = 0
	rl.inited = true
}

// Retain decides whether to emit `HASHER` or `BRANCH` for a given prefix, by
// checking if this is prefix of any of the keys added to the set
// Since keys in the set are sorted, and we expect that the prefixes will
// come in monotonically ascending order, we optimise for this, though
// the function would still work if the order is different
func (rl *RetainList) Retain(prefix []byte) bool {
	rl.ensureInited()
	if len(prefix) < rl.minLength {
		return true
	}
	// Adjust "GT" if necessary
	var gtAdjusted bool
	for rl.lteIndex < len(rl.hexes)-1 && bytes.Compare(rl.hexes[rl.lteIndex+1], prefix) <= 0 {
		rl.lteIndex++
		gtAdjusted = true
	}
	// Adjust "LTE" if necessary (normally will not be necessary)
	for !gtAdjusted && rl.lteIndex > 0 && bytes.Compare(rl.hexes[rl.lteIndex], prefix) > 0 {
		rl.lteIndex--
	}
	if rl.lteIndex < len(rl.hexes) {
		if bytes.HasPrefix(rl.hexes[rl.lteIndex], prefix) {
			return true
		}
	}
	if rl.lteIndex < len(rl.hexes)-1 {
		if bytes.HasPrefix(rl.hexes[rl.lteIndex+1], prefix) {
			return true
		}
	}
	return false
}

func (rl *RetainList) RetainWithMarker(prefix []byte) (bool, []byte) {
	rl.ensureInited()
	if len(prefix) < rl.minLength {
		return true, nil
	}
	// Adjust "GT" if necessary
	var gtAdjusted bool
	for rl.lteIndex < len(rl.hexes)-1 && bytes.Compare(rl.hexes[rl.lteIndex+1], prefix) <= 0 {
		rl.lteIndex++
		gtAdjusted = true
	}
	// Adjust "LTE" if necessary (normally will not be necessary)
	for !gtAdjusted && rl.lteIndex > 0 && bytes.Compare(rl.hexes[rl.lteIndex], prefix) > 0 {
		rl.lteIndex--
	}
	if rl.lteIndex < len(rl.hexes) {
		if bytes.HasPrefix(rl.hexes[rl.lteIndex], prefix) {
			return true, rl.nextMarkedItem(rl.lteIndex)
		}
	}
	if rl.lteIndex < len(rl.hexes)-1 {
		if bytes.HasPrefix(rl.hexes[rl.lteIndex+1], prefix) {
			return true, rl.nextMarkedItem(rl.lteIndex + 1)
		}
	}

	if rl.lteIndex < len(rl.hexes) {
		if bytes.Compare(prefix, rl.hexes[rl.lteIndex]) <= 0 {
			return false, rl.nextMarkedItem(rl.lteIndex)
		}
	}
	if rl.lteIndex < len(rl.hexes)-1 {
		if bytes.Compare(prefix, rl.hexes[rl.lteIndex+1]) <= 0 {
			return false, rl.nextMarkedItem(rl.lteIndex + 1)
		}
	}

	return false, nil
}

func (rl *RetainList) nextMarkedItem(index int) []byte {
	for i := index; i < len(rl.markers); i++ {
		if rl.markers[i] {
			return rl.hexes[i]
		}
	}
	return nil
}

// Rewind lets us reuse this list from the beginning
func (rl *RetainList) Rewind() {
	rl.lteIndex = 0
}

func (rl *RetainList) String() string {
	return fmt.Sprintf("%x", rl.hexes)
}
