package commitment

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/common/length"
)

// legacySharedHasher is the closure modules/state/commitment.makeSharedHasher
// returned before it switched to KeyToHexNibbleHash.
func legacySharedHasher() func([]byte) []byte {
	keccak := sha3.NewLegacyKeccak256()
	toNibbles := func(hasher hash.Hash, data []byte) []byte {
		hasher.Reset()
		hasher.Write(data)
		h := hasher.Sum(nil)
		nibbles := make([]byte, len(h)*2)
		for i, b := range h {
			nibbles[i*2] = b >> 4
			nibbles[i*2+1] = b & 0x0f
		}
		return nibbles
	}
	return func(key []byte) []byte {
		if len(key) == length.Addr {
			return toNibbles(keccak, key)
		}
		addr := toNibbles(keccak, key[:length.Addr])
		slot := toNibbles(keccak, key[length.Addr:])
		return append(addr, slot...)
	}
}

// TestKeyToHexNibbleHashMatchesLegacySharedHasher: the hasher the MPT root
// computer now hands to Updates must reproduce the old closure byte for byte
// for account keys, storage keys and every longer key the closure accepted.
func TestKeyToHexNibbleHashMatchesLegacySharedHasher(t *testing.T) {
	rnd := rand.New(rand.NewSource(1))
	legacy := legacySharedHasher()
	var cache addrHashCache
	for i := 0; i < 2000; i++ {
		n := length.Addr
		switch rnd.Intn(4) {
		case 0:
			n = length.Addr + length.Hash
		case 1:
			n = length.Addr + 1 + rnd.Intn(64)
		}
		key := make([]byte, n)
		rnd.Read(key)
		want := legacy(key)
		require.Equal(t, want, KeyToHexNibbleHash(key), "key %x", key)
		require.Equal(t, want, keyToHexNibbleHashCached(key, &cache), "cached key %x", key)
	}
}

// prevCheckingState wraps MockState and verifies, on every PutBranch, what
// the isNew shortcut in CollectUpdate/CollectDeferredUpdate assumes. When
// the encoder skipped the previous-value lookup (prev empty) while the store
// still held a record at that prefix — a delete marker, or a stale subtree
// left behind by an account deletion — the record the old code would have
// written is Merge(stored, data). That merge may only differ from data in
// the touchMap header, which every reader masks with afterMap or skips.
type prevCheckingState struct {
	*MockState
	t     *testing.T
	puts  int
	news  int
	stale int
}

func (s *prevCheckingState) PutBranch(prefix, data, prev []byte) error {
	stored, _, err := s.MockState.Branch(prefix)
	require.NoError(s.t, err)
	s.puts++
	if len(prev) == 0 {
		s.news++
		if len(stored) > 0 {
			s.stale++
			merged, err := (&BranchMerger{}).Merge(stored, data)
			require.NoError(s.t, err)
			if len(merged) < 4 || len(data) < 4 || !bytes.Equal(merged[2:], data[2:]) {
				s.t.Errorf("PutBranch %x: skipped prev %x; old code would have written %x, new writes %x", prefix, stored, merged, data)
			}
			if tm, am := binary.BigEndian.Uint16(data), binary.BigEndian.Uint16(data[2:]); tm&am != am {
				s.t.Errorf("PutBranch %x: new branch touchMap %04x does not cover afterMap %04x", prefix, tm, am)
			}
		}
	} else if !bytes.Equal(stored, prev) {
		s.t.Errorf("PutBranch %x: prev %x but store holds %x", prefix, prev, stored)
	}
	return s.MockState.PutBranch(prefix, data, prev)
}

func randomAddrs(rnd *rand.Rand, n int) []string {
	out := make([]string, n)
	for i := range out {
		b := make([]byte, length.Addr)
		rnd.Read(b)
		out[i] = hex.EncodeToString(b)
	}
	return out
}

// TestNewBranchSkipsPrevLookupSafely drives several commitments over a
// state that grows, loses accounts and storage (branches collapse) and
// regrows them (branches reappear at prefixes that once existed), checking
// the prev invariant on every branch write and that the final root equals a
// fresh trie built from the final state in one go. Both the direct and the
// deferred branch-encoding paths are exercised.
func TestNewBranchSkipsPrevLookupSafely(t *testing.T) {
	for _, deferUpdates := range []bool{false, true} {
		t.Run(fmt.Sprintf("defer=%v", deferUpdates), func(t *testing.T) {
			rnd := rand.New(rand.NewSource(99))
			ctx := context.Background()
			ms := NewMockState(t)
			cs := &prevCheckingState{MockState: ms, t: t}
			hph := NewHexPatriciaHashed(length.Addr, cs)
			hph.branchEncoder.SetDeferUpdates(deferUpdates)

			addrs := randomAddrs(rnd, 48)
			slots := make([]string, 8)
			for i := range slots {
				b := make([]byte, length.Hash)
				rnd.Read(b)
				slots[i] = hex.EncodeToString(b)
			}
			// final is the cumulative plain state used to rebuild from scratch
			finalBal := map[string]uint64{}
			finalStor := map[string]map[string]string{}
			var lastRoot []byte
			roundNo := 0
			freshTrie := func() (*MockState, *HexPatriciaHashed, []byte) {
				ub := NewUpdateBuilder()
				for a, v := range finalBal {
					ub.Balance(a, v)
				}
				for a, st := range finalStor {
					for s, v := range st {
						ub.Storage(a, s, v)
					}
				}
				plainKeys, updates := ub.Build()
				ms2 := NewMockState(t)
				require.NoError(t, ms2.applyPlainUpdates(plainKeys, updates))
				hph2 := NewHexPatriciaHashed(length.Addr, ms2)
				hph2.branchEncoder.SetDeferUpdates(deferUpdates)
				upds := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
				defer upds.Close()
				root, err := hph2.Process(ctx, upds, "", nil, WarmupConfig{})
				require.NoError(t, err)
				return ms2, hph2, root
			}
			round := func(build func(ub *UpdateBuilder)) {
				ub := NewUpdateBuilder()
				build(ub)
				plainKeys, updates := ub.Build()
				require.NoError(t, ms.applyPlainUpdates(plainKeys, updates))
				upds := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
				defer upds.Close()
				root, err := hph.Process(ctx, upds, "", nil, WarmupConfig{})
				require.NoError(t, err)
				hph.Reset()
				lastRoot = root
				roundNo++
				_, _, fresh := freshTrie()
				require.Equal(t, fresh, root, "round %d: incremental root differs from fresh rebuild", roundNo)
			}
			setBal := func(ub *UpdateBuilder, a string, v uint64) {
				ub.Balance(a, v)
				finalBal[a] = v
			}
			setStor := func(ub *UpdateBuilder, a, s, v string) {
				ub.Storage(a, s, v)
				if finalStor[a] == nil {
					finalStor[a] = map[string]string{}
				}
				finalStor[a][s] = v
			}
			del := func(ub *UpdateBuilder, a string) {
				ub.Delete(a)
				delete(finalBal, a)
				// the trie keys storage separately: an account deletion must
				// be accompanied by its storage deletions, as SELFDESTRUCT is
				for s := range finalStor[a] {
					ub.DeleteStorage(a, s)
				}
				delete(finalStor, a)
			}
			delStor := func(ub *UpdateBuilder, a, s string) {
				ub.DeleteStorage(a, s)
				if finalStor[a] != nil {
					delete(finalStor[a], s)
				}
			}
			// 1. create everything
			round(func(ub *UpdateBuilder) {
				for i, a := range addrs {
					setBal(ub, a, uint64(i+1))
					for j := 0; j < 1+i%4; j++ {
						setStor(ub, a, slots[j], fmt.Sprintf("%02x%02x", i, j))
					}
				}
			})
			// 2. delete every other account and most storage of the rest
			round(func(ub *UpdateBuilder) {
				for i, a := range addrs {
					if i%2 == 0 {
						del(ub, a)
					} else {
						for j := 1; j < 1+i%4; j++ {
							delStor(ub, a, slots[j])
						}
					}
				}
			})
			// 3. recreate the deleted accounts and storage at the same keys
			round(func(ub *UpdateBuilder) {
				for i, a := range addrs {
					if i%2 == 0 {
						setBal(ub, a, uint64(1000+i))
					}
					for j := 0; j < 1+i%4; j++ {
						setStor(ub, a, slots[j], fmt.Sprintf("ff%02x%02x", i, j))
					}
				}
			})
			// 4..8. random churn
			for r := 0; r < 5; r++ {
				round(func(ub *UpdateBuilder) {
					for _, a := range addrs {
						switch rnd.Intn(5) {
						case 0:
							del(ub, a)
						case 1:
							setBal(ub, a, uint64(rnd.Intn(1<<20)+1))
						case 2:
							s := slots[rnd.Intn(len(slots))]
							if _, ok := finalBal[a]; !ok {
								setBal(ub, a, 5)
							}
							setStor(ub, a, s, fmt.Sprintf("%08x", rnd.Uint32()))
						case 3:
							if st := finalStor[a]; len(st) > 0 {
								for s := range st {
									delStor(ub, a, s)
									break
								}
							}
						}
					}
				})
			}
			require.Greater(t, cs.puts, 0)
			require.Greater(t, cs.news, 0, "expected some branches to be created")
			require.Greater(t, cs.stale, 0, "expected some branches to be created over a stale record")

			// Rebuild the final state from scratch and compare roots and stores.
			ms2, _, fresh := freshTrie()
			require.Equal(t, fresh, lastRoot, "incremental root differs from fresh rebuild")
			// Every live branch of the fresh trie must be in the incremental
			// store with the same cells, modulo the touchMap header (the fresh
			// trie touched every cell exactly once). The incremental store may
			// additionally hold stale records: storage subtrees of deleted
			// accounts are never visited again, so their branches are not
			// deleted — that is pre-existing behaviour, and the stale-prev check
			// in PutBranch above covers what happens when they are overwritten.
			live := func(cm map[string]BranchData) map[string][]byte {
				out := map[string][]byte{}
				for k, v := range cm {
					if len(v) >= 4 && binary.BigEndian.Uint16(v[2:]) != 0 {
						out[k] = v[2:]
					}
				}
				return out
			}
			freshLive, inc := live(ms2.cm), live(ms.cm)
			require.LessOrEqual(t, len(freshLive), len(inc))
			for k, v := range freshLive {
				require.Equal(t, v, inc[k], "branch %x differs", k)
			}
		})
	}
}
