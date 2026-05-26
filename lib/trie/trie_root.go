package trie

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/bits"
	"time"

	"github.com/n42blockchain/N42/lib/log/v3"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/rlphacks"
)

/*
**Theoretically:** "Merkle trie root calculation" starts from state, build from state keys - trie,
on each level of trie calculates intermediate hash of underlying data.

**Practically:** It can be implemented as "Preorder trie traversal" (Preorder - visit Root, visit Left, visit Right).
But, let's make couple observations to make traversal over huge state efficient.

**Observation 1:** `TrieOfAccounts` already stores state keys in sorted way.
Iteration over this bucket will retrieve keys in same order as "Preorder trie traversal".

**Observation 2:** each Eth block - changes not big part of state - it means most of Merkle trie intermediate hashes will not change.
It means we effectively can cache them. `TrieOfAccounts` stores "Intermediate hashes of all Merkle trie levels".
It also sorted and Iteration over `TrieOfAccounts` will retrieve keys in same order as "Preorder trie traversal".

**Implementation:** by opening 1 Cursor on state and 1 more Cursor on intermediate hashes bucket - we will receive data in
 order of "Preorder trie traversal". Cursors will only do "sequential reads" and "jumps forward" - been hardware-friendly.
1 stack keeps all accumulated hashes, when sub-trie traverse ends - all hashes pulled from stack -> hashed -> new hash puts on stack - it's hash of visited sub-trie (it emulates recursive nature of "Preorder trie traversal" algo).

Imagine that account with key 0000....00 (64 zeroes, 32 bytes of zeroes) changed.
Here is an example sequence which can be seen by running 2 Cursors:
```
00   // key which came from cache, can't use it - because account with this prefix changed
0000 // key which came from cache, can't use it - because account with this prefix changed
...
{30 zero bytes}00    // key which came from cache, can't use it - because account with this prefix changed
{30 zero bytes}0000  // Account which came from state, use it - calculate hash, jump to "next sub-trie"
{30 zero bytes}01    // key which came from cache, it is "next sub-trie", use it, jump to "next sub-trie"
{30 zero bytes}02    // key which came from cache, it is "next sub-trie", use it, jump to "next sub-trie"
...
{30 zero bytes}ff    // key which came from cache, it is "next sub-trie", use it, jump to "next sub-trie"
{29 zero bytes}01    // key which came from cache, it is "next sub-trie" (1 byte shorter key), use it, jump to "next sub-trie"
{29 zero bytes}02    // key which came from cache, it is "next sub-trie" (1 byte shorter key), use it, jump to "next sub-trie"
...
ff                   // key which came from cache, it is "next sub-trie" (1 byte shorter key), use it, jump to "next sub-trie"
nil                  // db returned nil - means no more keys there, done
```
On practice Trie is not full - it means after account key `{30 zero bytes}0000` may come `{5 zero bytes}01` and amount of iterations will not be big.

### Attack - by delete account with huge state

It's possible to create Account with very big storage (increase storage size during many blocks).
Then delete this account (SELFDESTRUCT).
 Naive storage deletion may take several minutes - depends on Disk speed - means every Eth client
 will not process any incoming block that time. To protect against this attack:
 PlainState, HashedState and IntermediateTrieHash buckets have "incarnations". Account entity has field "Incarnation" -
 just a digit which increasing each SELFDESTRUCT or CREATE2 opcodes. Storage key formed by:
 `{account_key}{incarnation}{storage_hash}`. And [turbo/trie/trie_root.go](../../turbo/trie/trie_root.go) has logic - every time
 when Account visited - we save it to `accAddrHashWithInc` variable and skip any Storage or IntermediateTrieHashes with another incarnation.
*/

// FlatDBTrieLoader reads state and intermediate trie hashes in order equal to "Preorder trie traversal"
// (Preorder - visit Root, visit Left, visit Right)
//
// It produces stream of values and send this stream to `receiver`
// It skips storage with incorrect incarnations
//
// Each intermediate hash key firstly pass to RetainDecider, only if it returns "false" - such AccTrie can be used.
type FlatDBTrieLoader struct {
	logPrefix          string
	trace              bool
	rd                 RetainDeciderWithMarker
	accAddrHashWithInc [32]byte // addrHash of the currently-built account (incarnation removed — storage keys are 32B addrHash, matching reth)

	ihSeek, accSeek, storageSeek []byte
	kHex, kHexS                  []byte

	// Account item buffer
	accountValue account.StateAccount

	receiver *RootHashAggregator
	hc       HashCollector2
	shc      StorageHashCollector2
}

// RootHashAggregator - calculates Merkle trie root hash from incoming data stream
type RootHashAggregator struct {
	trace          bool
	wasIH          bool
	wasIHStorage   bool
	root           types.Hash
	hc             HashCollector2
	shc            StorageHashCollector2
	currStorage    bytes.Buffer // Current key for the structure generation algorithm, as well as the input tape for the hash builder
	succStorage    bytes.Buffer
	valueStorage   []byte // Current value to be used as the value tape for the hash builder
	hadTreeStorage bool
	hashAccount    types.Hash // Current value to be used as the value tape for the hash builder
	hashStorage    types.Hash // Current value to be used as the value tape for the hash builder
	curr           bytes.Buffer   // Current key for the structure generation algorithm, as well as the input tape for the hash builder
	succ           bytes.Buffer
	currAccK       []byte
	value          []byte // Current value to be used as the value tape for the hash builder
	hadTreeAcc     bool
	groups         []uint16 // `groups` parameter is the map of the stack. each element of the `groups` slice is a bitmask, one bit per element currently on the stack. See `GenStructStep` docs
	hasTree        []uint16
	hasHash        []uint16
	groupsStorage  []uint16 // `groups` parameter is the map of the stack. each element of the `groups` slice is a bitmask, one bit per element currently on the stack. See `GenStructStep` docs
	hasTreeStorage []uint16
	hasHashStorage []uint16
	hb             *HashBuilder
	hashData       GenStructStepHashData
	a              account.StateAccount
	leafData       GenStructStepLeafData
	accData        GenStructStepAccountData

	// Used to construct an Account proof while calculating the tree root.
	proofRetainer *ProofRetainer
	cutoff        bool
}

func NewRootHashAggregator() *RootHashAggregator {
	return &RootHashAggregator{
		hb: NewHashBuilder(false),
	}
}

func NewFlatDBTrieLoader(logPrefix string, rd RetainDeciderWithMarker, hc HashCollector2, shc StorageHashCollector2, trace bool) *FlatDBTrieLoader {
	if trace {
		fmt.Printf("----------\n")
		fmt.Printf("CalcTrieRoot\n")
	}
	return &FlatDBTrieLoader{
		logPrefix: logPrefix,
		receiver: &RootHashAggregator{
			hb:    NewHashBuilder(false),
			hc:    hc,
			shc:   shc,
			trace: trace,
		},
		ihSeek:      make([]byte, 0, 128),
		accSeek:     make([]byte, 0, 128),
		storageSeek: make([]byte, 0, 128),
		kHex:        make([]byte, 0, 128),
		kHexS:       make([]byte, 0, 128),
		rd:          rd,
		hc:          hc,
		shc:         shc,
	}
}

func (l *FlatDBTrieLoader) SetProofRetainer(pr *ProofRetainer) {
	l.receiver.proofRetainer = pr
}

// CalcTrieRoot algo:
//
//		for iterateIHOfAccounts {
//			if canSkipState
//	         goto SkipAccounts
//
//			for iterateAccounts from prevIH to currentIH {
//				use(account)
//				for iterateIHOfStorage within accountWithIncarnation{
//					if canSkipState
//						goto SkipStorage
//
//					for iterateStorage from prevIHOfStorage to currentIHOfStorage {
//						use(storage)
//					}
//	           SkipStorage:
//					use(ihStorage)
//				}
//			}
//	   SkipAccounts:
//			use(AccTrie)
//		}
func (l *FlatDBTrieLoader) CalcTrieRoot(tx kv.Tx, quit <-chan struct{}) (types.Hash, error) {

	accC, err := tx.Cursor(kv.HashedAccounts)
	if err != nil {
		return EmptyRoot, err
	}
	defer accC.Close()
	accs := NewStateCursor(accC, quit)
	trieAccC, err := tx.Cursor(kv.TrieOfAccounts)
	if err != nil {
		return EmptyRoot, err
	}
	defer trieAccC.Close()
	trieStorageC, err := tx.CursorDupSort(kv.TrieOfStorage)
	if err != nil {
		return EmptyRoot, err
	}
	defer trieStorageC.Close()

	var canUse = func(prefix []byte) (bool, []byte) {
		retain, nextCreated := l.rd.RetainWithMarker(prefix)
		return !retain, nextCreated
	}
	accTrie := AccTrie(canUse, l.hc, trieAccC, quit)
	storageTrie := StorageTrie(canUse, l.shc, trieStorageC, quit)

	ss, err := tx.CursorDupSort(kv.HashedStorage)
	if err != nil {
		return EmptyRoot, err
	}
	defer ss.Close()
	logEvery := time.NewTicker(30 * time.Second)
	defer logEvery.Stop()
	for ihK, ihV, hasTree, err := accTrie.AtPrefix(nil); ; ihK, ihV, hasTree, err = accTrie.Next() { // no loop termination is at he end of loop
		if err != nil {
			return EmptyRoot, err
		}
		var firstPrefix []byte
		var done bool
		if accTrie.SkipState {
			goto SkipAccounts
		}

		firstPrefix, done = accTrie.FirstNotCoveredPrefix()
		if done {
			goto SkipAccounts
		}

		for k, kHex, v, err1 := accs.Seek(firstPrefix); k != nil; k, kHex, v, err1 = accs.Next() {
			if err1 != nil {
				return EmptyRoot, err1
			}
			if keyIsBefore(ihK, kHex) {
				break
			}
			if err = l.accountValue.DecodeForStorage(v); err != nil {
				return EmptyRoot, fmt.Errorf("fail DecodeForStorage: %w", err)
			}
			if err = l.receiver.Receive(AccountStreamItem, kHex, nil, &l.accountValue, nil, nil, false, 0); err != nil {
				return EmptyRoot, err
			}
			copy(l.accAddrHashWithInc[:], k)
			// incarnation removed: storage trie key prefix is the 32B addrHash only
			accWithInc := l.accAddrHashWithInc[:]
			for ihKS, ihVS, hasTreeS, err2 := storageTrie.SeekToAccount(accWithInc); ; ihKS, ihVS, hasTreeS, err2 = storageTrie.Next() {
				if err2 != nil {
					return EmptyRoot, err2
				}

				if storageTrie.skipState {
					goto SkipStorage
				}

				firstPrefix, done = storageTrie.FirstNotCoveredPrefix()
				if done {
					goto SkipStorage
				}

				for vS, err3 := ss.SeekBothRange(accWithInc, firstPrefix); vS != nil; _, vS, err3 = ss.NextDup() {
					if err3 != nil {
						return EmptyRoot, err3
					}
					hexutil.DecompressNibbles(vS[:32], &l.kHexS)
					if keyIsBefore(ihKS, l.kHexS) { // read until next AccTrie
						break
					}
					if err = l.receiver.Receive(StorageStreamItem, accWithInc, l.kHexS, nil, vS[32:], nil, false, 0); err != nil {
						return EmptyRoot, err
					}
				}

			SkipStorage:
				if ihKS == nil { // Loop termination
					break
				}

				if err = l.receiver.Receive(SHashStreamItem, accWithInc, ihKS, nil, nil, ihVS, hasTreeS, 0); err != nil {
					return EmptyRoot, err
				}
				if len(ihKS) == 0 { // means we just sent acc.storageRoot
					break
				}
			}

			select {
			default:
			case <-logEvery.C:
				l.logProgress(k, ihK)
			}
		}

	SkipAccounts:
		if ihK == nil { // Loop termination
			break
		}

		if err = l.receiver.Receive(AHashStreamItem, ihK, nil, nil, nil, ihV, hasTree, 0); err != nil {
			return EmptyRoot, err
		}
	}

	if err := l.receiver.Receive(CutoffStreamItem, nil, nil, nil, nil, nil, false, 0); err != nil {
		return EmptyRoot, err
	}
	return l.receiver.Root(), nil
}

func (l *FlatDBTrieLoader) logProgress(accountKey, ihK []byte) {
	var k string
	if accountKey != nil {
		k = makeCurrentKeyStr(accountKey)
	} else if ihK != nil {
		k = makeCurrentKeyStr(ihK)
	}
	log.Info(fmt.Sprintf("[%s] Calculating Merkle root", l.logPrefix), "current key", k)
}

func (r *RootHashAggregator) RetainNothing(_ []byte) bool {
	return false
}

func (r *RootHashAggregator) Receive(itemType StreamItem,
	accountKey []byte,
	storageKey []byte,
	accountValue *account.StateAccount,
	storageValue []byte,
	hash []byte,
	hasTree bool,
	cutoff int,
) error {
	//r.traceIf("9c3dc2561d472d125d8f87dde8f2e3758386463ade768ae1a1546d34101968bb", "00")
	//if storageKey == nil {
	//	//if bytes.HasPrefix(accountKey, types.FromHex("08050d07")) {
	//	fmt.Printf("1: %d, %x, %x\n", itemType, accountKey, hash)
	//	//}
	//} else {
	//	//if bytes.HasPrefix(accountKey, types.FromHex("876f5a0f54b30254d2bad26bb5a8da19cbe748fd033004095d9c96c8e667376b")) && bytes.HasPrefix(storageKey, types.FromHex("")) {
	//	//fmt.Printf("%x\n", storageKey)
	//	fmt.Printf("1: %d, %x, %x, %x\n", itemType, accountKey, storageKey, hash)
	//	//}
	//}
	//

	switch itemType {
	case StorageStreamItem:
		if len(r.currAccK) == 0 {
			r.currAccK = append(r.currAccK[:0], accountKey...)
		}
		r.advanceKeysStorage(storageKey, true /* terminator */)
		if r.currStorage.Len() > 0 {
			if err := r.genStructStorage(); err != nil {
				return err
			}
		}
		r.saveValueStorage(false, hasTree, storageValue, hash)
	case SHashStreamItem:
		if len(storageKey) == 0 { // this is ready-to-use storage root - no reason to call GenStructStep, also GenStructStep doesn't support empty prefixes
			r.hb.hashStack = append(append(r.hb.hashStack, byte(80+length.Hash)), hash...)
			r.hb.nodeStack = append(r.hb.nodeStack, nil)
			r.accData.FieldSet |= AccountFieldStorageOnly
			break
		}
		if len(r.currAccK) == 0 {
			r.currAccK = append(r.currAccK[:0], accountKey...)
		}
		r.advanceKeysStorage(storageKey, false /* terminator */)
		if r.currStorage.Len() > 0 {
			if err := r.genStructStorage(); err != nil {
				return err
			}
		}
		r.saveValueStorage(true, hasTree, storageValue, hash)
	case AccountStreamItem:
		r.advanceKeysAccount(accountKey, true /* terminator */)
		if r.curr.Len() > 0 && !r.wasIH {
			r.cutoffKeysStorage(0)
			if r.currStorage.Len() > 0 {
				if err := r.genStructStorage(); err != nil {
					return err
				}
			}
			if r.currStorage.Len() > 0 {
				r.groupsStorage = r.groupsStorage[:0]
				r.hasTreeStorage = r.hasTreeStorage[:0]
				r.hasHashStorage = r.hasHashStorage[:0]
				r.currStorage.Reset()
				r.succStorage.Reset()
				r.wasIHStorage = false
				// There are some storage items
				r.accData.FieldSet |= AccountFieldStorageOnly
			}
		}
		r.currAccK = r.currAccK[:0]
		if r.curr.Len() > 0 {
			if err := r.genStructAccount(); err != nil {
				return err
			}
		}
		if err := r.saveValueAccount(false, hasTree, accountValue, hash); err != nil {
			return err
		}
	case AHashStreamItem:
		r.advanceKeysAccount(accountKey, false /* terminator */)
		if r.curr.Len() > 0 && !r.wasIH {
			r.cutoffKeysStorage(0)
			if r.currStorage.Len() > 0 {
				if err := r.genStructStorage(); err != nil {
					return err
				}
			}
			if r.currStorage.Len() > 0 {
				r.groupsStorage = r.groupsStorage[:0]
				r.hasTreeStorage = r.hasTreeStorage[:0]
				r.hasHashStorage = r.hasHashStorage[:0]
				r.currStorage.Reset()
				r.succStorage.Reset()
				r.wasIHStorage = false
				// There are some storage items
				r.accData.FieldSet |= AccountFieldStorageOnly
			}
		}
		r.currAccK = r.currAccK[:0]
		if r.curr.Len() > 0 {
			if err := r.genStructAccount(); err != nil {
				return err
			}
		}
		if err := r.saveValueAccount(true, hasTree, accountValue, hash); err != nil {
			return err
		}
	case CutoffStreamItem:
		if r.trace {
			fmt.Printf("storage cuttoff %d\n", cutoff)
		}
		r.cutoffKeysAccount(cutoff)
		if r.curr.Len() > 0 && !r.wasIH {
			r.cutoffKeysStorage(0)
			if r.currStorage.Len() > 0 {
				if err := r.genStructStorage(); err != nil {
					return err
				}
			}
			if r.currStorage.Len() > 0 {
				r.groupsStorage = r.groupsStorage[:0]
				r.hasTreeStorage = r.hasTreeStorage[:0]
				r.hasHashStorage = r.hasHashStorage[:0]
				r.currStorage.Reset()
				r.succStorage.Reset()
				r.wasIHStorage = false
				// There are some storage items
				r.accData.FieldSet |= AccountFieldStorageOnly
			}
		}

		// Used for optional GetProof calculation to trigger inclusion of the top-level node
		r.cutoff = true

		if r.curr.Len() > 0 {
			if err := r.genStructAccount(); err != nil {
				return err
			}
		}
		if r.curr.Len() > 0 {
			if len(r.groups) > cutoff {
				r.groups = r.groups[:cutoff]
				r.hasTree = r.hasTree[:cutoff]
				r.hasHash = r.hasHash[:cutoff]
			}
		}
		if r.hb.hasRoot() {
			r.root = r.hb.rootHash()
		} else {
			r.root = EmptyRoot
		}
		r.groups = r.groups[:0]
		r.hasTree = r.hasTree[:0]
		r.hasHash = r.hasHash[:0]
		r.hb.Reset()
		r.wasIH = false
		r.wasIHStorage = false
		r.curr.Reset()
		r.succ.Reset()
		r.currStorage.Reset()
		r.succStorage.Reset()
	}
	return nil
}

//nolint
// func (r *RootHashAggregator) traceIf(acc, st string) {
// 	// "succ" - because on this iteration this "succ" will become "curr"
// 	if r.succStorage.Len() == 0 {
// 		var accNibbles []byte
// 		hexutil.DecompressNibbles(types.FromHex(acc), &accNibbles)
// 		r.trace = bytes.HasPrefix(r.succ.Bytes(), accNibbles)
// 	} else {
// 		r.trace = bytes.HasPrefix(r.currAccK, types.FromHex(acc)) && bytes.HasPrefix(r.succStorage.Bytes(), types.FromHex(st))
// 	}
// }

func (r *RootHashAggregator) Root() types.Hash {
	return r.root
}

func (r *RootHashAggregator) advanceKeysStorage(k []byte, terminator bool) {
	r.currStorage.Reset()
	r.currStorage.Write(r.succStorage.Bytes())
	r.succStorage.Reset()
	// Transform k to nibbles, but skip the incarnation part in the middle
	r.succStorage.Write(k)

	if terminator {
		r.succStorage.WriteByte(16)
	}
}

func (r *RootHashAggregator) cutoffKeysStorage(cutoff int) {
	r.currStorage.Reset()
	r.currStorage.Write(r.succStorage.Bytes())
	r.succStorage.Reset()
	//if r.currStorage.Len() > 0 {
	//r.succStorage.Write(r.currStorage.Bytes()[:cutoff-1])
	//r.succStorage.WriteByte(r.currStorage.Bytes()[cutoff-1] + 1) // Modify last nibble in the incarnation part of the `currStorage`
	//}
}

func (r *RootHashAggregator) genStructStorage() error {
	var err error
	var data GenStructStepData
	if r.wasIHStorage {
		r.hashData.Hash = r.hashStorage
		r.hashData.HasTree = r.hadTreeStorage
		data = &r.hashData
	} else {
		r.leafData.Value = rlphacks.RlpSerializableBytes(r.valueStorage)
		data = &r.leafData
	}
	var wantProof func(_ []byte) *proofElement
	if r.proofRetainer != nil {
		// incarnation removed: storage key = addrHash(32B) nibbles + storage prefix
		var fullKey [2 * (length.Hash + length.Hash)]byte
		for i, b := range r.currAccK {
			fullKey[i*2] = b / 16
			fullKey[i*2+1] = b % 16
		}
		baseKeyLen := 2 * length.Hash
		wantProof = func(prefix []byte) *proofElement {
			copy(fullKey[baseKeyLen:], prefix)
			return r.proofRetainer.ProofElement(fullKey[:baseKeyLen+len(prefix)])
		}
	}
	r.groupsStorage, r.hasTreeStorage, r.hasHashStorage, err = GenStructStepEx(r.RetainNothing, r.currStorage.Bytes(), r.succStorage.Bytes(), r.hb, func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if r.shc == nil {
			return nil
		}
		return r.shc(r.currAccK, keyHex, hasState, hasTree, hasHash, hashes, rootHash)
	}, data, r.groupsStorage, r.hasTreeStorage, r.hasHashStorage,
		r.trace,
		wantProof,
		r.cutoff,
	)
	if err != nil {
		return err
	}
	return nil
}

func (r *RootHashAggregator) saveValueStorage(isIH, hasTree bool, v, h []byte) {
	// Remember the current value
	r.wasIHStorage = isIH
	r.valueStorage = nil
	if isIH {
		r.hashStorage.SetBytes(h)
		r.hadTreeStorage = hasTree
	} else {
		r.valueStorage = v
	}
}

func (r *RootHashAggregator) advanceKeysAccount(k []byte, terminator bool) {
	r.curr.Reset()
	r.curr.Write(r.succ.Bytes())
	r.succ.Reset()
	r.succ.Write(k)
	if terminator {
		r.succ.WriteByte(16)
	}
}

func (r *RootHashAggregator) cutoffKeysAccount(cutoff int) {
	r.curr.Reset()
	r.curr.Write(r.succ.Bytes())
	r.succ.Reset()
	if r.curr.Len() > 0 && cutoff > 0 {
		r.succ.Write(r.curr.Bytes()[:cutoff-1])
		r.succ.WriteByte(r.curr.Bytes()[cutoff-1] + 1) // Modify last nibble before the cutoff point
	}
}

func (r *RootHashAggregator) genStructAccount() error {
	var data GenStructStepData
	if r.wasIH {
		r.hashData.Hash = r.hashAccount
		r.hashData.HasTree = r.hadTreeAcc
		data = &r.hashData
	} else {
		r.accData.Balance.Set(&r.a.Balance)
		if !r.a.Balance.IsZero() {
			r.accData.FieldSet |= AccountFieldBalanceOnly
		}
		r.accData.Nonce = r.a.Nonce
		if r.a.Nonce != 0 {
			r.accData.FieldSet |= AccountFieldNonceOnly
		}
		r.accData.Incarnation = 0
		data = &r.accData
	}
	r.wasIHStorage = false
	r.currStorage.Reset()
	r.succStorage.Reset()
	var err error

	var wantProof func(_ []byte) *proofElement
	if r.proofRetainer != nil {
		wantProof = r.proofRetainer.ProofElement
	}
	if r.groups, r.hasTree, r.hasHash, err = GenStructStepEx(r.RetainNothing, r.curr.Bytes(), r.succ.Bytes(), r.hb, func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if r.hc == nil {
			return nil
		}
		return r.hc(keyHex, hasState, hasTree, hasHash, hashes, rootHash)
	}, data, r.groups, r.hasTree, r.hasHash,
		//false,
		r.trace,
		wantProof,
		r.cutoff,
	); err != nil {
		return err
	}
	r.accData.FieldSet = 0
	return nil
}

func (r *RootHashAggregator) saveValueAccount(isIH, hasTree bool, v *account.StateAccount, h []byte) error {
	r.wasIH = isIH
	if isIH {
		r.hashAccount.SetBytes(h)
		r.hadTreeAcc = hasTree
		return nil
	}
	r.a.Copy(v)
	// Place code on the stack first, the storage will follow
	if !r.a.IsEmptyCodeHash() {
		// the first item ends up deepest on the stack, the second item - on the top
		r.accData.FieldSet |= AccountFieldCodeOnly
		if err := r.hb.hash(r.a.CodeHash[:]); err != nil {
			return err
		}
	}
	return nil
}

// AccTrieCursor - holds logic related to iteration over AccTrie bucket
// has 2 basic operations:  _preOrderTraversalStep and _preOrderTraversalStepNoInDepth
type AccTrieCursor struct {
	SkipState       bool
	lvl             int
	k, v            [64][]byte // store up to 64 levels of key/value pairs in nibbles format
	hasState        [64]uint16 // says that records in dbutil.HashedAccounts exists by given prefix
	hasTree         [64]uint16 // says that records in dbutil.TrieOfAccounts exists by given prefix
	hasHash         [64]uint16 // store ownership of hashes stored in .v
	childID, hashID [64]int8   // meta info: current child in .hasState[lvl] field, max child id, current hash in .v[lvl]
	deleted         [64]bool   // helper to avoid multiple deletes of same key

	c               kv.Cursor
	hc              HashCollector2
	prev, cur, next []byte
	prefix          []byte // global prefix - cursor will never return records without this prefix

	firstNotCoveredPrefix []byte
	canUse                func([]byte) (bool, []byte) // if this function returns true - then this AccTrie can be used as is and don't need continue PostorderTraversal, but switch to sibling instead
	nextCreated           []byte

	kBuf []byte
	quit <-chan struct{}
}

func AccTrie(canUse func([]byte) (bool, []byte), hc HashCollector2, c kv.Cursor, quit <-chan struct{}) *AccTrieCursor {
	return &AccTrieCursor{
		c:                     c,
		canUse:                canUse,
		firstNotCoveredPrefix: make([]byte, 0, 64),
		next:                  make([]byte, 0, 64),
		kBuf:                  make([]byte, 0, 64),
		hc:                    hc,
		quit:                  quit,
	}
}

// _preOrderTraversalStep - goToChild || nextSiblingInMem || nextSiblingOfParentInMem || nextSiblingInDB
func (c *AccTrieCursor) _preOrderTraversalStep() error {
	if c._hasTree() {
		c.next = append(append(c.next[:0], c.k[c.lvl]...), byte(c.childID[c.lvl]))
		ok, err := c._seek(c.next, c.next)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return c._preOrderTraversalStepNoInDepth()
}

// _preOrderTraversalStepNoInDepth - nextSiblingInMem || nextSiblingOfParentInMem || nextSiblingInDB
func (c *AccTrieCursor) _preOrderTraversalStepNoInDepth() error {
	ok := c._nextSiblingInMem() || c._nextSiblingOfParentInMem()
	if ok {
		return nil
	}
	err := c._nextSiblingInDB()
	if err != nil {
		return err
	}
	return nil
}

func (c *AccTrieCursor) FirstNotCoveredPrefix() ([]byte, bool) {
	var ok bool
	c.firstNotCoveredPrefix, ok = firstNotCoveredPrefix(c.prev, c.prefix, c.firstNotCoveredPrefix)
	return c.firstNotCoveredPrefix, ok
}

func (c *AccTrieCursor) AtPrefix(prefix []byte) (k, v []byte, hasTree bool, err error) {
	c.SkipState = false // There can be accounts with keys less than the first key in AccTrie
	_, c.nextCreated = c.canUse([]byte{})
	c.prev = append(c.prev[:0], c.cur...)
	c.prefix = prefix
	ok, err := c._seek(prefix, []byte{})
	if err != nil {
		return []byte{}, nil, false, err
	}
	if !ok {
		c.cur = nil
		c.SkipState = false
		return nil, nil, false, nil
	}
	ok, err = c._consume()
	if err != nil {
		return []byte{}, nil, false, err
	}
	if ok {
		return c.cur, c._hash(c.hashID[c.lvl]), c._hasTree(), nil
	}

	return c._next()
}

func (c *AccTrieCursor) Next() (k, v []byte, hasTree bool, err error) {
	c.SkipState = true
	c.prev = append(c.prev[:0], c.cur...)
	err = c._preOrderTraversalStepNoInDepth()
	if err != nil {
		return []byte{}, nil, false, err
	}
	if c.k[c.lvl] == nil {
		c.cur = nil
		c.SkipState = c.SkipState && !dbutils2.NextNibblesSubtree(c.prev, &c.next)
		return nil, nil, false, nil
	}
	ok, err := c._consume()
	if err != nil {
		return []byte{}, nil, false, err
	}
	if ok {
		return c.cur, c._hash(c.hashID[c.lvl]), c._hasTree(), nil
	}

	return c._next()
}

func (c *AccTrieCursor) _seek(seek []byte, withinPrefix []byte) (bool, error) {
	var k, v []byte
	var err error
	if len(seek) == 0 {
		k, v, err = c.c.First()
	} else {
		//TODO: write more common optimization - maintain .canUseNext variable by hasTree info - similar to skipState
		// optimistic .Next call, can use result in 2 cases:
		// - k is not child of current key
		// - looking for first child, means: c.childID[c.lvl] <= int16(bits.TrailingZeros16(c.hasTree[c.lvl]))
		// otherwise do .Seek call
		//k, v, err = c.c.Next()
		//if err != nil {
		//	return false, err
		//}
		//if bytes.HasPrefix(k, c.k[c.lvl]) {
		//	c.is++
		k, v, err = c.c.Seek(seek)
		//}
	}
	if err != nil {
		return false, err
	}
	if len(withinPrefix) > 0 { // seek within given prefix must not terminate overall process, even if k==nil
		if k == nil {
			return false, nil
		}
		if !bytes.HasPrefix(k, withinPrefix) {
			return false, nil
		}
	} else { // seek over global prefix does terminate overall process
		if k == nil {
			c.k[c.lvl] = nil
			return false, nil
		}
		if !bytes.HasPrefix(k, c.prefix) {
			c.k[c.lvl] = nil
			return false, nil
		}
	}
	c._unmarshal(k, v)
	c._nextSiblingInMem()
	return true, nil
}

func (c *AccTrieCursor) _nextSiblingInMem() bool {
	for c.childID[c.lvl] < int8(bits.Len16(c.hasState[c.lvl])) {
		c.childID[c.lvl]++
		if c._hasHash() {
			c.hashID[c.lvl]++
			return true
		}
		if c._hasTree() {
			return true
		}
		if c._hasState() {
			c.SkipState = false
		}
	}
	return false
}

func (c *AccTrieCursor) _nextSiblingOfParentInMem() bool {
	originalLvl := c.lvl
	for c.lvl > 1 {
		c.lvl--
		if c.k[c.lvl] == nil {
			continue
		}
		c.next = append(append(c.next[:0], c.k[originalLvl]...), uint8(c.childID[originalLvl]))
		c.kBuf = append(append(c.kBuf[:0], c.k[c.lvl]...), uint8(c.childID[c.lvl]))
		ok, err := c._seek(c.next, c.kBuf)
		if err != nil {
			panic(err)
		}
		if ok {
			return true
		}
		if c._nextSiblingInMem() {
			return true
		}
		originalLvl = c.lvl
	}
	return false
}

func (c *AccTrieCursor) _nextSiblingInDB() error {
	ok := dbutils2.NextNibblesSubtree(c.k[c.lvl], &c.next)
	if !ok {
		c.k[c.lvl] = nil
		return nil
	}
	if _, err := c._seek(c.next, []byte{}); err != nil {
		return err
	}
	if c.k[c.lvl] == nil || !bytes.HasPrefix(c.next, c.k[c.lvl]) {
		// If the cursor has moved beyond the next subtree, we need to check to make
		// sure that any modified keys in between are processed.
		c.SkipState = false
	}
	return nil
}

func (c *AccTrieCursor) _unmarshal(k, v []byte) {
	from, to := c.lvl+1, len(k)
	if c.lvl >= len(k) {
		from, to = len(k)+1, c.lvl+2
	}

	// Consider a trie DB with keys like: [0xa, 0xbb], then unmarshaling 0xbb
	// needs to nil the existing 0xa key entry, as it is no longer a parent.
	for i := from - 1; i > 0; i-- {
		if c.k[i] == nil {
			continue
		}
		if bytes.HasPrefix(k, c.k[i]) {
			break
		}
		from = i
	}
	for i := from; i < to; i++ { // if first meet key is not 0 length, then nullify all shorter metadata
		c.k[i], c.hasState[i], c.hasTree[i], c.hasHash[i], c.hashID[i], c.childID[i], c.deleted[i] = nil, 0, 0, 0, 0, 0, false
	}
	c.lvl = len(k)
	c.k[c.lvl] = k
	c.deleted[c.lvl] = false
	c.hasState[c.lvl], c.hasTree[c.lvl], c.hasHash[c.lvl], c.v[c.lvl], _ = UnmarshalTrieNode(v)
	c.hashID[c.lvl] = -1
	c.childID[c.lvl] = int8(bits.TrailingZeros16(c.hasState[c.lvl]) - 1)
}

func (c *AccTrieCursor) _deleteCurrent() error {
	if c.hc == nil {
		return nil
	}
	if c.hc == nil || c.deleted[c.lvl] {
		return nil
	}
	if err := c.hc(c.k[c.lvl], 0, 0, 0, nil, nil); err != nil {
		return err
	}
	c.deleted[c.lvl] = true
	return nil
}

func (c *AccTrieCursor) _hasState() bool { return (1<<c.childID[c.lvl])&c.hasState[c.lvl] != 0 }
func (c *AccTrieCursor) _hasTree() bool  { return (1<<c.childID[c.lvl])&c.hasTree[c.lvl] != 0 }
func (c *AccTrieCursor) _hasHash() bool  { return (1<<c.childID[c.lvl])&c.hasHash[c.lvl] != 0 }
func (c *AccTrieCursor) _hash(i int8) []byte {
	return c.v[c.lvl][length.Hash*int(i) : length.Hash*(int(i)+1)]
}

func (c *AccTrieCursor) _consume() (bool, error) {
	if c._hasHash() {
		c.kBuf = append(append(c.kBuf[:0], c.k[c.lvl]...), uint8(c.childID[c.lvl]))
		if ok, nextCreated := c.canUse(c.kBuf); ok {
			c.SkipState = c.SkipState && keyIsBefore(c.kBuf, c.nextCreated)
			c.nextCreated = nextCreated
			c.cur = append(c.cur[:0], c.kBuf...)
			return true, nil
		}
	}

	if err := c._deleteCurrent(); err != nil {
		return false, err
	}

	return false, nil
}

func (c *AccTrieCursor) _next() (k, v []byte, hasTree bool, err error) {
	var ok bool
	if err = libtypes.Stopped(c.quit); err != nil {
		return []byte{}, nil, false, err
	}
	c.SkipState = c.SkipState && c._hasTree()
	err = c._preOrderTraversalStep()
	if err != nil {
		return []byte{}, nil, false, err
	}

	for {
		if c.k[c.lvl] == nil {
			c.cur = nil
			c.SkipState = c.SkipState && !dbutils2.NextNibblesSubtree(c.prev, &c.next)
			return nil, nil, false, nil
		}

		ok, err = c._consume()
		if err != nil {
			return []byte{}, nil, false, err
		}
		if ok {
			return c.cur, c._hash(c.hashID[c.lvl]), c._hasTree(), nil
		}

		c.SkipState = c.SkipState && c._hasTree()
		err = c._preOrderTraversalStep()
		if err != nil {
			return []byte{}, nil, false, err
		}
	}
}

// StorageTrieCursor - holds logic related to iteration over AccTrie bucket
type StorageTrieCursor struct {
	lvl                        int
	k, v                       [64][]byte
	hasState, hasTree, hasHash [64]uint16
	deleted                    [64]bool
	childID, hashID            [64]int8

	c         kv.Cursor
	shc       StorageHashCollector2
	prev, cur []byte
	seek      []byte
	root      []byte

	next                  []byte
	firstNotCoveredPrefix []byte
	canUse                func([]byte) (bool, []byte)
	nextCreated           []byte
	skipState             bool

	accWithInc []byte
	kBuf       []byte
	quit       <-chan struct{}
}

func StorageTrie(canUse func(prefix []byte) (bool, []byte), shc StorageHashCollector2, c kv.Cursor, quit <-chan struct{}) *StorageTrieCursor {
	ih := &StorageTrieCursor{c: c, canUse: canUse,
		firstNotCoveredPrefix: make([]byte, 0, 64),
		next:                  make([]byte, 0, 64),
		kBuf:                  make([]byte, 0, 64),
		shc:                   shc,
		quit:                  quit,
	}
	return ih

}

func (c *StorageTrieCursor) PrevKey() []byte {
	return c.prev
}

func (c *StorageTrieCursor) FirstNotCoveredPrefix() ([]byte, bool) {
	var ok bool
	c.firstNotCoveredPrefix, ok = firstNotCoveredPrefix(c.prev, []byte{0, 0}, c.firstNotCoveredPrefix)
	return c.firstNotCoveredPrefix, ok
}

func (c *StorageTrieCursor) SeekToAccount(accWithInc []byte) (k, v []byte, hasTree bool, err error) {
	c.skipState = true
	// Reset per-account level state. erigon always writes an empty-path
	// account.root record whose _unmarshal (path is empty) nils every stale
	// level entry from the previous account, so its cursor is implicitly reset
	// between accounts. reth storage tries omit that record, so the first
	// record has a non-empty path and _unmarshal's prefix check can RETAIN a
	// previous account's level entries when paths coincidentally share a
	// prefix — corrupting this account's storage root. Clear explicitly.
	for i := range c.k {
		c.k[i] = nil
		c.v[i] = nil
		c.hasState[i] = 0
		c.hasTree[i] = 0
		c.hasHash[i] = 0
		c.deleted[i] = false
		c.childID[i] = 0
		c.hashID[i] = 0
	}
	c.lvl = 0
	c.cur = nil
	c.next = c.next[:0]
	c.accWithInc = accWithInc
	hexutil.DecompressNibbles(c.accWithInc, &c.kBuf)
	_, c.nextCreated = c.canUse(c.kBuf)
	c.seek = append(c.seek[:0], c.accWithInc...)
	c.prev = c.cur
	var ok bool
	ok, err = c._seek(accWithInc, []byte{})
	if err != nil {
		return []byte{}, nil, false, err
	}
	if !ok {
		c.cur = nil
		c.skipState = false
		return nil, nil, false, nil
	}
	if c.root != nil && c.lvl == 0 { // real empty-path root record: acc.storageRoot usable
		root := c.root
		c.root = nil
		ok1, nextCreated := c.canUse(c.kBuf)
		if ok1 {
			c.skipState = true
			c.nextCreated = nextCreated
			c.cur = c.k[c.lvl]
			return c.cur, root, false, nil
		}
		err = c._deleteCurrent()
		if err != nil {
			return []byte{}, nil, false, err
		}
		err = c._preOrderTraversalStepNoInDepth()
		if err != nil {
			return []byte{}, nil, false, err
		}
	} else {
		// No empty-path account.root record for this storage trie. erigon always
		// writes one (it carries the whole-trie storage root + its tree_mask /
		// hash_mask), but reth omits it. Without it the cursor has no lvl-0 frame:
		// it would process only the first top-level branch record and terminate,
		// computing the storage root from a fraction of the trie.
		//
		// Walk like reth instead: synthesize a virtual lvl-0 root. Scan the
		// path-1 branch records to recover the root's tree_mask (which top-level
		// children are stored branches to descend into). hasState is set to all-1s
		// so every other top-level child is leaf-scanned (skipState=false), and
		// hasHash=0 so the root's own hash is recomputed from its children rather
		// than read from a (non-existent) cache. prev is reset so the first
		// FirstNotCoveredPrefix returns {0,0} (start of THIS account's storage),
		// not NextNibblesSubtree of the previous account's slot path.
		// See modules/state/commitment/trie_root_trace_test.go (TestRethShape*).
		var treeMask uint16
		var probe [33]byte
		copy(probe[:32], c.accWithInc)
		for nib := byte(0); nib < 16; nib++ {
			probe[32] = nib
			sk, _, e := c.c.Seek(probe[:])
			if e != nil {
				return []byte{}, nil, false, e
			}
			if len(sk) == 33 && bytes.Equal(sk, probe[:]) {
				treeMask |= 1 << nib
			}
		}
		for i := range c.k {
			c.k[i], c.v[i] = nil, nil
			c.hasState[i], c.hasTree[i], c.hasHash[i] = 0, 0, 0
			c.deleted[i], c.childID[i], c.hashID[i] = false, 0, 0
		}
		c.lvl = 0
		c.k[0] = []byte{}
		c.hasState[0] = 0xFFFF
		c.hasTree[0] = treeMask
		c.hasHash[0] = 0
		c.childID[0] = int8(bits.TrailingZeros16(c.hasState[0]) - 1)
		c.hashID[0] = -1
		c.skipState = false
		c.prev = c.prev[:0]
		c._nextSiblingInMem()
	}

	ok, err = c._consume()
	if err != nil {
		return []byte{}, nil, false, err
	}
	if ok {
		return c.cur, c._hash(c.hashID[c.lvl]), c._hasTree(), nil
	}

	return c._next()
}

func (c *StorageTrieCursor) Next() (k, v []byte, hasTree bool, err error) {
	c.skipState = true
	c.prev = c.cur
	err = c._preOrderTraversalStepNoInDepth()
	if err != nil {
		return []byte{}, nil, false, err
	}
	if c.k[c.lvl] == nil {
		c.skipState = c.skipState && !dbutils2.NextNibblesSubtree(c.prev, &c.next)
		c.cur = nil
		return nil, nil, false, nil
	}

	ok, err := c._consume()
	if err != nil {
		return []byte{}, nil, false, err
	}
	if ok {
		return c.cur, c._hash(c.hashID[c.lvl]), c._hasTree(), nil
	}
	return c._next()
}

func (c *StorageTrieCursor) _consume() (bool, error) {
	if c._hasHash() {
		c.kBuf = append(append(c.kBuf[:64], c.k[c.lvl]...), uint8(c.childID[c.lvl]))
		ok, nextCreated := c.canUse(c.kBuf)
		if ok {
			c.skipState = c.skipState && keyIsBefore(c.kBuf, c.nextCreated)
			c.nextCreated = nextCreated
			c.cur = types.CopyBytes(c.kBuf[64:])
			return true, nil
		}
	}

	if err := c._deleteCurrent(); err != nil {
		return false, err
	}
	return false, nil
}

func (c *StorageTrieCursor) _seek(seek, withinPrefix []byte) (bool, error) {
	k, v, err := c.c.Seek(seek)
	if err != nil {
		return false, err
	}
	if len(withinPrefix) > 0 { // seek within given prefix must not terminate overall process
		if k == nil {
			return false, nil
		}
		if !bytes.HasPrefix(k, c.accWithInc) || !bytes.HasPrefix(k[32:], withinPrefix) {
			return false, nil
		}
	} else {
		if k == nil {
			c.k[c.lvl] = nil
			return false, nil
		}
		if !bytes.HasPrefix(k, c.accWithInc) {
			c.k[c.lvl] = nil
			return false, nil
		}
	}
	c._unmarshal(k, v)
	if c.lvl > 0 { // root record, firstly storing root hash
		c._nextSiblingInMem()
	}
	return true, nil
}

// _preOrderTraversalStep - goToChild || nextSiblingInMem || nextSiblingOfParentInMem || nextSiblingInDB
func (c *StorageTrieCursor) _preOrderTraversalStep() error {
	if c._hasTree() {
		c.seek = append(append(c.seek[:32], c.k[c.lvl]...), byte(c.childID[c.lvl]))
		ok, err := c._seek(c.seek, []byte{})
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return c._preOrderTraversalStepNoInDepth()
}

// _preOrderTraversalStepNoInDepth - nextSiblingInMem || nextSiblingOfParentInMem || nextSiblingInDB
func (c *StorageTrieCursor) _preOrderTraversalStepNoInDepth() error {
	ok := c._nextSiblingInMem() || c._nextSiblingOfParentInMem()
	if ok {
		return nil
	}
	err := c._nextSiblingInDB()
	if err != nil {
		return err
	}
	return nil
}

func (c *StorageTrieCursor) _hasState() bool { return (1<<c.childID[c.lvl])&c.hasState[c.lvl] != 0 }
func (c *StorageTrieCursor) _hasHash() bool  { return (1<<c.childID[c.lvl])&c.hasHash[c.lvl] != 0 }
func (c *StorageTrieCursor) _hasTree() bool  { return (1<<c.childID[c.lvl])&c.hasTree[c.lvl] != 0 }
func (c *StorageTrieCursor) _hash(i int8) []byte {
	return c.v[c.lvl][int(i)*length.Hash : (int(i)+1)*length.Hash]
}

func (c *StorageTrieCursor) _nextSiblingInMem() bool {
	for c.childID[c.lvl] < int8(bits.Len16(c.hasState[c.lvl])) {
		c.childID[c.lvl]++
		if c._hasHash() {
			c.hashID[c.lvl]++
			return true
		}
		if c._hasTree() {
			return true
		}
		if c._hasState() {
			c.skipState = false
		}
	}
	return false
}

func (c *StorageTrieCursor) _nextSiblingOfParentInMem() bool {
	originalLvl := c.lvl
	for c.lvl > 0 {
		c.lvl--
		if c.k[c.lvl] == nil {
			continue
		}

		c.seek = append(append(c.seek[:32], c.k[originalLvl]...), uint8(c.childID[originalLvl]))
		c.next = append(append(c.next[:0], c.k[c.lvl]...), uint8(c.childID[c.lvl]))
		ok, err := c._seek(c.seek, c.next)
		if err != nil {
			panic(err)
		}
		if ok {
			return true
		}
		if c._nextSiblingInMem() {
			return true
		}
		originalLvl = c.lvl
	}
	return false
}

func (c *StorageTrieCursor) _nextSiblingInDB() error {
	ok := dbutils2.NextNibblesSubtree(c.k[c.lvl], &c.next)
	if !ok {
		c.k[c.lvl] = nil
		return nil
	}
	c.seek = append(c.seek[:32], c.next...)
	if _, err := c._seek(c.seek, []byte{}); err != nil {
		return err
	}
	if c.k[c.lvl] == nil || !bytes.HasPrefix(c.next, c.k[c.lvl]) {
		// If the cursor has moved beyond the next subtree, we need to check to make
		// sure that any modified keys in between are processed.
		c.skipState = false
	}
	return nil
}

func (c *StorageTrieCursor) _next() (k, v []byte, hasTree bool, err error) {
	var ok bool
	if err = libtypes.Stopped(c.quit); err != nil {
		return []byte{}, nil, false, err
	}
	c.skipState = c.skipState && c._hasTree()
	if err = c._preOrderTraversalStep(); err != nil {
		return []byte{}, nil, false, err
	}

	for {
		if c.k[c.lvl] == nil {
			c.cur = nil
			c.skipState = c.skipState && !dbutils2.NextNibblesSubtree(c.prev, &c.next)
			return nil, nil, false, nil
		}

		ok, err = c._consume()
		if err != nil {
			return []byte{}, nil, false, err
		}
		if ok {
			return c.cur, c._hash(c.hashID[c.lvl]), c._hasTree(), nil
		}

		c.skipState = c.skipState && c._hasTree()
		err = c._preOrderTraversalStep()
		if err != nil {
			return []byte{}, nil, false, err
		}
	}
}

func (c *StorageTrieCursor) _unmarshal(k, v []byte) {
	from, to := c.lvl+1, len(k)
	if c.lvl >= len(k) {
		from, to = len(k)+1, c.lvl+2
	}
	// Consider a trie DB with keys like: [0xa, 0xbb], then unmarshaling 0xbb
	// needs to nil the existing 0xa key entry, as it is no longer a parent.
	for i := from - 1; i > 0; i-- {
		if c.k[i] == nil {
			continue
		}
		if bytes.HasPrefix(k[32:], c.k[i]) {
			break
		}
		from = i
	}
	for i := from; i < to; i++ { // if first meet key is not 0 length, then nullify all shorter metadata
		c.k[i], c.hasState[i], c.hasTree[i], c.hasHash[i], c.hashID[i], c.childID[i], c.deleted[i] = nil, 0, 0, 0, 0, 0, false
	}

	c.lvl = len(k) - 32
	c.k[c.lvl] = k[32:]
	c.deleted[c.lvl] = false
	c.hasState[c.lvl], c.hasTree[c.lvl], c.hasHash[c.lvl], c.v[c.lvl], c.root = UnmarshalTrieNode(v)
	c.hashID[c.lvl] = -1
	c.childID[c.lvl] = int8(bits.TrailingZeros16(c.hasState[c.lvl]) - 1)
}

func (c *StorageTrieCursor) _deleteCurrent() error {
	if c.shc == nil {
		return nil
	}
	if c.shc == nil || c.deleted[c.lvl] {
		return nil
	}
	if err := c.shc(c.accWithInc, c.k[c.lvl], 0, 0, 0, nil, nil); err != nil {
		return err
	}
	c.deleted[c.lvl] = true
	return nil
}

/*
Dense Sequence - if between 2 AccTrie records not possible insert any state record - then they form "dense sequence"
If 2 AccTrie records form Dense Sequence - then no reason to iterate over state - just use AccTrie one after another
Example1:

	1234
	1235

Example2:

	12ff
	13

Example3:

	12ff
	13000000

If 2 AccTrie records form "sequence" then it can be consumed without moving StateCursor
*/
func isDenseSequence(prev []byte, next []byte) bool {
	isSequence := false
	if len(prev) == 0 && len(next) == 0 {
		return false
	}
	ok := dbutils2.NextNibblesSubtree(prev, &isSequenceBuf)
	if len(prev) > 0 && !ok {
		return true
	}
	if bytes.HasPrefix(next, isSequenceBuf) {
		tail := next[len(isSequenceBuf):] // if tail has only zeroes, then no state records can be between fstl.nextHex and fstl.ihK
		isSequence = true
		for _, n := range tail {
			if n != 0 {
				isSequence = false
				break
			}
		}
	}

	return isSequence
}

var isSequenceBuf = make([]byte, 256)

func firstNotCoveredPrefix(prev, prefix, buf []byte) ([]byte, bool) {
	if len(prev) > 0 {
		if !dbutils2.NextNibblesSubtree(prev, &buf) {
			return buf, true
		}
	} else {
		buf = append(buf[:0], prefix...)
	}
	if len(buf)%2 == 1 {
		buf = append(buf, 0)
	}
	hexutil.CompressNibbles(buf, &buf)
	return buf, false
}

type StateCursor struct {
	c    kv.Cursor
	quit <-chan struct{}
	kHex []byte
}

func NewStateCursor(c kv.Cursor, quit <-chan struct{}) *StateCursor {
	return &StateCursor{c: c, quit: quit}
}

func (c *StateCursor) Seek(seek []byte) ([]byte, []byte, []byte, error) {
	k, v, err := c.c.Seek(seek)
	if err != nil {
		return []byte{}, nil, nil, err
	}

	hexutil.DecompressNibbles(k, &c.kHex)
	return k, c.kHex, v, nil
}

func (c *StateCursor) Next() ([]byte, []byte, []byte, error) {
	if err := libtypes.Stopped(c.quit); err != nil {
		return []byte{}, nil, nil, err
	}
	k, v, err := c.c.Next()
	if err != nil {
		return []byte{}, nil, nil, err
	}

	hexutil.DecompressNibbles(k, &c.kHex)
	return k, c.kHex, v, nil
}

// keyIsBefore - ksind of bytes.Compare, but nil is the last key. And return
func keyIsBefore(k1, k2 []byte) bool {
	if k1 == nil {
		return false
	}
	if k2 == nil {
		return true
	}
	return bytes.Compare(k1, k2) < 0
}

func UnmarshalTrieNodeTyped(v []byte) (hasState, hasTree, hasHash uint16, hashes []types.Hash, rootHash types.Hash) {
	hasState, hasTree, hasHash, v = binary.BigEndian.Uint16(v), binary.BigEndian.Uint16(v[2:]), binary.BigEndian.Uint16(v[4:]), v[6:]
	if bits.OnesCount16(hasHash)+1 == len(v)/length.Hash {
		rootHash.SetBytes(libtypes.CopyBytes(v[:32]))
		v = v[32:]
	}
	hashes = make([]types.Hash, len(v)/length.Hash)
	for i := 0; i < len(hashes); i++ {
		hashes[i].SetBytes(libtypes.CopyBytes(v[i*length.Hash : (i+1)*length.Hash]))
	}
	return
}

func UnmarshalTrieNode(v []byte) (hasState, hasTree, hasHash uint16, hashes, rootHash []byte) {
	hasState, hasTree, hasHash, hashes = binary.BigEndian.Uint16(v), binary.BigEndian.Uint16(v[2:]), binary.BigEndian.Uint16(v[4:]), v[6:]
	if bits.OnesCount16(hasHash)+1 == len(hashes)/length.Hash {
		rootHash = hashes[:32]
		hashes = hashes[32:]
	}
	return
}

func MarshalTrieNodeTyped(hasState, hasTree, hasHash uint16, h []types.Hash, buf []byte) []byte {
	buf = buf[:6+len(h)*length.Hash]
	meta, hashes := buf[:6], buf[6:]
	binary.BigEndian.PutUint16(meta, hasState)
	binary.BigEndian.PutUint16(meta[2:], hasTree)
	binary.BigEndian.PutUint16(meta[4:], hasHash)
	for i := 0; i < len(h); i++ {
		copy(hashes[i*length.Hash:(i+1)*length.Hash], h[i].Bytes())
	}
	return buf
}

func StorageKey(addressHash []byte, incarnation uint64, prefix []byte) []byte {
	return dbutils2.GenerateCompositeStoragePrefix(addressHash, incarnation, prefix)
}

func MarshalTrieNode(hasState, hasTree, hasHash uint16, hashes, rootHash []byte, buf []byte) []byte {
	buf = buf[:len(hashes)+len(rootHash)+6]
	meta, hashesList := buf[:6], buf[6:]
	binary.BigEndian.PutUint16(meta, hasState)
	binary.BigEndian.PutUint16(meta[2:], hasTree)
	binary.BigEndian.PutUint16(meta[4:], hasHash)
	if len(rootHash) == 0 {
		copy(hashesList, hashes)
	} else {
		copy(hashesList, rootHash)
		copy(hashesList[32:], hashes)
	}
	return buf
}

// MarshalTrieNodeDense encodes a branch node with FULL slot data:
// the variable-length RLP encoding of every present child (either a
// 32-byte hash reference 0xa0||hash, or inline RLP starting with
// 0xc0..0xfe). Unlike MarshalTrieNode, the dense form preserves
// inline children's exact bytes — sufficient to reproduce the
// parent's RLP and serve EIP-1186 proofs without any rebuild.
//
// Layout (big-endian where multibyte):
//
//	+0  state_mask    uint16   bit i set = child i exists
//	+2  tree_mask     uint16   bit i set = child i is a deeper branch
//	+4  for each set bit in state_mask, ascending:
//	      first byte of the child slot's hashStack entry, followed by
//	      its payload. Total slot length = (b0 == 0xa0 ? 33 : b0-0xc0+1).
//
// slotData is the raw hashStack[] block that branchHash receives
// (hashStackStride * popcount(state_mask) bytes, 33 per slot).
func MarshalTrieNodeDense(stateMask, treeMask uint16, slotData []byte, buf []byte) []byte {
	// Compute output size: 4-byte header + per-slot variable bytes.
	const stride = 33 // hashStackStride
	size := 4
	digits := bits.OnesCount16(stateMask)
	for i := 0; i < digits; i++ {
		b0 := slotData[i*stride]
		size += slotLenFromPrefix(b0)
	}
	buf = ensureCap(buf, size)[:size]
	binary.BigEndian.PutUint16(buf[0:2], stateMask)
	binary.BigEndian.PutUint16(buf[2:4], treeMask)
	off := 4
	for i := 0; i < digits; i++ {
		b0 := slotData[i*stride]
		n := slotLenFromPrefix(b0)
		copy(buf[off:off+n], slotData[i*stride:i*stride+n])
		off += n
	}
	return buf
}

// UnmarshalTrieNodeDense parses a dense branch encoding into the
// state mask, tree mask, and 16 per-child slot byte slices (nil for
// empty). Slot bytes alias `buf` — do not modify; copy if you need
// long-lived storage.
func UnmarshalTrieNodeDense(buf []byte) (stateMask, treeMask uint16, slots [16][]byte, err error) {
	if len(buf) < 4 {
		err = fmt.Errorf("MarshalTrieNodeDense: too short (%d bytes)", len(buf))
		return
	}
	stateMask = binary.BigEndian.Uint16(buf[0:2])
	treeMask = binary.BigEndian.Uint16(buf[2:4])
	off := 4
	for digit := 0; digit < 16; digit++ {
		if stateMask&(1<<digit) == 0 {
			continue
		}
		if off >= len(buf) {
			err = fmt.Errorf("MarshalTrieNodeDense: truncated at digit %d (off=%d len=%d)", digit, off, len(buf))
			return
		}
		n := slotLenFromPrefix(buf[off])
		if off+n > len(buf) {
			err = fmt.Errorf("MarshalTrieNodeDense: slot %d overruns buf (need %d at off=%d len=%d)", digit, n, off, len(buf))
			return
		}
		slots[digit] = buf[off : off+n]
		off += n
	}
	if off != len(buf) {
		err = fmt.Errorf("MarshalTrieNodeDense: %d trailing bytes after slots", len(buf)-off)
		return
	}
	return
}

// slotLenFromPrefix returns the total byte length of one child slot
// given its first byte. 0x01 = G2 LeafMarker (1B). 0xa0 = hash ref
// (33B). 0xc0..0xfe = short list (b0 - 0xc0 + 1 bytes total). 0x80 =
// empty (1B). We never expect 0x80 in slot data (empty slots aren't
// pushed to hashStack) but handle it defensively.
func slotLenFromPrefix(b0 byte) int {
	if b0 == LeafMarker { // 0x01 — G2 marker
		return 1
	}
	if b0 == 0xa0 {
		return 33
	}
	if b0 >= 0xc0 && b0 <= 0xfe {
		return int(b0-0xc0) + 1
	}
	if b0 == 0x80 {
		return 1
	}
	// 0x81..0xb7 are short strings; not expected as child slots in our
	// MPT (children are always lists or hash refs), but length is
	// b0 - 0x80 + 1.
	if b0 >= 0x81 && b0 <= 0xb7 {
		return int(b0-0x80) + 1
	}
	// Default — treat as 1 byte to avoid runaway parse.
	return 1
}

func ensureCap(buf []byte, n int) []byte {
	if cap(buf) >= n {
		return buf
	}
	return make([]byte, 0, n)
}

// LeafMarker is the V2 dense-encoding marker for a slot that
// references a single LEAF resolvable by prefix-scanning a hashed
// state table at the slot's nibble path. Distinct from any byte that
// V1's encoding produces in slot position (0x80 empty, 0xa0 hash ref,
// 0xc0..0xfe inline RLP).
const LeafMarker byte = 0x01

// MarshalTrieNodeDenseV2 is the G2 variant of MarshalTrieNodeDense.
// Slots whose hash represents a single LEAF — state bit set, tree
// bit clear, extension bit clear, original slot prefix == 0xa0 —
// are emitted as a 1-byte LeafMarker. The reader reconstructs the
// 33-byte 0xa0||hash via the base hashed table.
//
// extMask (G2 Option A): bit i set iff the slot's stack entry was
// produced by extensionHash, i.e. the stored hash is keccak(
// extension_RLP) and CANNOT be re-derived from the leaf alone.
// Those slots stay as 33-byte 0xa0||hash verbatim.
//
// Container header is 2 B (state_mask only). Tree / ext info is not
// stored — read side can't see those bits but doesn't need to (it
// dispatches off slot first byte: 0x01 marker ⇒ leaf; 0xa0 ⇒ hash
// ref; 0xc0..0xfe ⇒ inline RLP).
func MarshalTrieNodeDenseV2(stateMask, treeMask, extMask uint16, slotData []byte, buf []byte) []byte {
	const stride = 33
	size := 2 // 2 B state_mask
	{
		i := 0
		for digit := uint(0); digit < 16; digit++ {
			if stateMask&(1<<digit) == 0 {
				continue
			}
			b0 := slotData[i*stride]
			isLeafHash := b0 == 0xa0 && (treeMask&(1<<digit)) == 0 && (extMask&(1<<digit)) == 0
			if isLeafHash {
				size++
			} else {
				size += slotLenFromPrefix(b0)
			}
			i++
		}
	}
	buf = ensureCap(buf, size)[:size]
	binary.BigEndian.PutUint16(buf[0:2], stateMask)
	off := 2
	i := 0
	for digit := uint(0); digit < 16; digit++ {
		if stateMask&(1<<digit) == 0 {
			continue
		}
		b0 := slotData[i*stride]
		isLeafHash := b0 == 0xa0 && (treeMask&(1<<digit)) == 0 && (extMask&(1<<digit)) == 0
		if isLeafHash {
			buf[off] = LeafMarker
			off++
		} else {
			n := slotLenFromPrefix(b0)
			copy(buf[off:off+n], slotData[i*stride:i*stride+n])
			off += n
		}
		i++
	}
	return buf
}

// UnmarshalTrieNodeDenseV2 parses a V2-encoded dense branch. The
// returned `treeMask` is INFERRED from slot first bytes: a bit is
// set iff the corresponding slot's first byte is 0xa0 (33-byte hash
// reference to a deeper branch). LeafMarker and inline slots leave
// the tree bit clear.
//
// Slots marked LeafMarker (0x01) are returned as 1-byte slices — the
// reader is responsible for expanding them via the base hashed table
// before emitting proof bytes.
func UnmarshalTrieNodeDenseV2(buf []byte) (stateMask, treeMask uint16, slots [16][]byte, err error) {
	if len(buf) < 2 {
		err = fmt.Errorf("UnmarshalTrieNodeDenseV2: too short (%d bytes)", len(buf))
		return
	}
	stateMask = binary.BigEndian.Uint16(buf[0:2])
	off := 2
	for digit := 0; digit < 16; digit++ {
		if stateMask&(1<<digit) == 0 {
			continue
		}
		if off >= len(buf) {
			err = fmt.Errorf("UnmarshalTrieNodeDenseV2: truncated at digit %d", digit)
			return
		}
		b0 := buf[off]
		n := slotLenFromPrefix(b0)
		if off+n > len(buf) {
			err = fmt.Errorf("UnmarshalTrieNodeDenseV2: slot %d overruns buf", digit)
			return
		}
		slots[digit] = buf[off : off+n]
		off += n
		// Infer tree bit: only 0xa0-prefixed 33-byte slots are deeper
		// branches. LeafMarker (0x01) and inline (0xc0..) are leaves.
		if b0 == 0xa0 {
			treeMask |= 1 << digit
		}
	}
	if off != len(buf) {
		err = fmt.Errorf("UnmarshalTrieNodeDenseV2: %d trailing bytes", len(buf)-off)
		return
	}
	return
}

// IsLeafMarker reports whether a slot from UnmarshalTrieNodeDenseV2
// is the V2 1-byte LeafMarker (caller must expand via the base).
func IsLeafMarker(slot []byte) bool {
	return len(slot) == 1 && slot[0] == LeafMarker
}

func CastTrieNodeValue(hashes, rootHash []byte) []types.Hash {
	to := make([]types.Hash, len(hashes)/length.Hash+len(rootHash)/length.Hash)
	i := 0
	if len(rootHash) > 0 {
		to[0].SetBytes(libtypes.CopyBytes(rootHash))
		i++
	}
	for j := 0; j < len(hashes)/length.Hash; j++ {
		to[i].SetBytes(libtypes.CopyBytes(hashes[j*length.Hash : (j+1)*length.Hash]))
		i++
	}
	return to
}

// CalcRoot is a combination of `ResolveStateTrie` and `UpdateStateTrie`
// DESCRIBED: docs/programmers_guide/guide.md#organising-ethereum-state-into-a-merkle-tree
func CalcRoot(logPrefix string, tx kv.Tx) (types.Hash, error) {
	loader := NewFlatDBTrieLoader(logPrefix, NewRetainList(0), nil, nil, false)

	h, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return EmptyRoot, err
	}

	return h, nil
}

func makeCurrentKeyStr(k []byte) string {
	var currentKeyStr string
	if k == nil {
		currentKeyStr = "final"
	} else if len(k) < 4 {
		currentKeyStr = hex.EncodeToString(k)
	} else {
		currentKeyStr = hex.EncodeToString(k[:4])
	}
	return currentKeyStr
}

func isSequenceOld(prev []byte, next []byte) bool {
	isSequence := false
	if bytes.HasPrefix(next, prev) {
		tail := next[len(prev):] // if tail has only zeroes, then no state records can be between fstl.nextHex and fstl.ihK
		isSequence = true
		for _, n := range tail {
			if n != 0 {
				isSequence = false
				break
			}
		}
	}

	return isSequence
}
