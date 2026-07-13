package trie

import (
	"fmt"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
)

// CalcTrieRootShard computes the depth-1 subtrie hash for a SINGLE top account
// nibble (0..15) using the exact same INCREMENTAL cursor path as CalcTrieRoot
// (HashedAccounts + TrieOfAccounts/TrieOfStorage cache + RetainList skipping),
// but with the account cursor bounded to the nibble and the aggregator finished
// at cutoff=1 (so it yields the depth-1 subtrie hash, not the full root).
//
// Running all 16 shards — each on its OWN read transaction — concurrently and
// folding the 16 results with CombineNibbleSubtries reproduces CalcTrieRoot's
// root BYTE-FOR-BYTE. This is the incremental-path analogue of the streaming
// CalcTrieRootStreamingCutoff(…,1) shards used by internal/ethel's parallel
// full-state-root, and the building block for a parallel per-window DATC root.
//
// Returns EmptyRoot for an absent nibble (no accounts under it); callers should
// map EmptyRoot -> nil before passing the slice to CombineNibbleSubtries.
//
// PRECONDITION (load-bearing): the account trie's top node must be a clean
// 16-branch — i.e. ≥2 top nibbles are populated, so there is no top-level
// extension/leaf collapse. Always true for non-trivial keccak-distributed
// mainnet state. If VIOLATED (empty/tiny/single-nibble-dominated tries, common
// in unit tests or very early blocks), CombineNibbleSubtries rebuilds a
// synthetic 16-branch top and returns a hash that DIFFERS from CalcTrieRoot
// with NO error — a silent wrong root. Any build/consumer that wires this in
// MUST guard against it: require the combined root to pass the per-window
// gold-check against the header (DATC already does), AND/OR fall back to serial
// CalcTrieRoot when fewer than 2 nibbles are non-empty. Do not trust the
// parallel root unconditionally.
//
// This is a PROTOTYPE: it proves byte-identity of the cursor-driven nibble-shard
// + combine against the serial loader; it is not yet wired into the build.
func (l *FlatDBTrieLoader) CalcTrieRootShard(tx kv.Tx, nibble byte, quit <-chan struct{}) (types.Hash, error) {
	if nibble > 15 {
		return EmptyRoot, fmt.Errorf("CalcTrieRootShard: nibble %d out of range [0,15]", nibble)
	}

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

	prefix := []byte{nibble} // single-nibble global prefix bounds the account cursor

	for ihK, ihV, hasTree, err := accTrie.AtPrefix(prefix); ; ihK, ihV, hasTree, err = accTrie.Next() {
		if err != nil {
			return EmptyRoot, err
		}
		var firstPrefix []byte
		var k, kHex, v []byte
		var err1 error
		var done bool
		if accTrie.SkipState {
			goto SkipAccounts
		}

		firstPrefix, done = accTrie.FirstNotCoveredPrefix()
		if done {
			goto SkipAccounts
		}

		k, kHex, v, err1 = accs.Seek(firstPrefix)
		if err1 != nil {
			return EmptyRoot, fmt.Errorf("account state seek: %w", err1)
		}
		for k != nil {
			if len(kHex) == 0 || kHex[0] != nibble {
				break // left the shard's nibble range (AccTrie prefix bound does not bound the state cursor)
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
			accWithInc := l.accAddrHashWithInc[:]
			for ihKS, ihVS, hasTreeS, err2 := storageTrie.SeekToAccount(accWithInc); ; ihKS, ihVS, hasTreeS, err2 = storageTrie.Next() {
				var vS []byte
				var err3 error
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

				vS, err3 = ss.SeekBothRange(accWithInc, firstPrefix)
				if err3 != nil {
					return EmptyRoot, fmt.Errorf("storage state seek: %w", err3)
				}
				for vS != nil {
					hexutil.DecompressNibbles(vS[:32], &l.kHexS)
					if keyIsBefore(ihKS, l.kHexS) {
						break
					}
					if err = l.receiver.Receive(StorageStreamItem, accWithInc, l.kHexS, nil, vS[32:], nil, false, 0); err != nil {
						return EmptyRoot, err
					}
					_, vS, err3 = ss.NextDup()
					if err3 != nil {
						return EmptyRoot, fmt.Errorf("storage state next: %w", err3)
					}
				}

			SkipStorage:
				if ihKS == nil {
					break
				}
				if err = l.receiver.Receive(SHashStreamItem, accWithInc, ihKS, nil, nil, ihVS, hasTreeS, 0); err != nil {
					return EmptyRoot, err
				}
				if len(ihKS) == 0 {
					break
				}
			}
			k, kHex, v, err1 = accs.Next()
			if err1 != nil {
				return EmptyRoot, fmt.Errorf("account state next: %w", err1)
			}
		}

	SkipAccounts:
		if ihK == nil {
			break
		}
		if err = l.receiver.Receive(AHashStreamItem, ihK, nil, nil, nil, ihV, hasTree, 0); err != nil {
			return EmptyRoot, err
		}
	}

	// cutoff=1: emit the depth-1 subtrie hash (the node the top branch's child for
	// this nibble points to), to be folded by CombineNibbleSubtries.
	if err := l.receiver.Receive(CutoffStreamItem, nil, nil, nil, nil, nil, false, 1); err != nil {
		return EmptyRoot, err
	}
	return l.receiver.Root(), nil
}
