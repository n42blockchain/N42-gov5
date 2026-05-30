// n42-multiproof-measure measures the real byte size of an MPT multiproof for a
// simulated block changeset, directly from a migrated N42 trie datadir
// (D:/N42-hashed: HashedAccount/HashedStorage + TrieAccount/TrieStorage). It
// bypasses the mptproof/mpttrie high-level API (and its Meta-bucket / leaf-source
// requirements) — it just reads BranchNodeCompact nodes by prefix with raw mdbx
// cursors, which is all a multiproof is.
//
// For K sampled accounts (+ S slots each) it collects the proof path = every
// TrieAccount node whose nibble key is a prefix of keccak(addr), and the
// per-account TrieStorage nodes on the slot paths. Then reports:
//   - naive   : sum of per-key path bytes (no sharing)
//   - dedup   : unique nodes (multiproof — shared upper branches counted once)
//   - zstd    : dedup bytes after zstd
//   - per trie-depth: node count + bytes + share of nodes that are "upper"
//     (depth ≤ 2, the part that changes ~every block and can't be reused)
//
//	n42-multiproof-measure --dir D:/N42-hashed/chaindata --accts 600 --slots 5
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/bits"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	tHashedAcc = "HashedAccount"
	tHashedSto = "HashedStorage"
	tTrieAcc   = "TrieAccount"
	tTrieSto   = "TrieStorage"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d[tHashedAcc] = kv.TableCfgItem{}
	d[tHashedSto] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 64, DupToLen: 32}
	d[tTrieAcc] = kv.TableCfgItem{}
	d[tTrieSto] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

// toNibbles expands bytes to 1-nibble-per-byte (matches rtrie keyHex).
func toNibbles(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, x := range b {
		out[2*i] = x >> 4
		out[2*i+1] = x & 0x0f
	}
	return out
}

// hashCount parses a BranchNodeCompact value and returns how many 32B child
// hashes it carries (popcount(hasHash), +1 if it embeds an own/rootHash).
func hashCount(v []byte) int {
	if len(v) < 6 {
		return 0
	}
	hasHash := binary.BigEndian.Uint16(v[4:6])
	n := bits.OnesCount16(hasHash)
	if (len(v)-6)/32 == n+1 {
		n++ // embedded rootHash
	}
	return n
}

type stats struct {
	naive     int
	nodes     map[string][]byte // dedup: nibble-key -> node bytes
	depthCnt  map[int]int
	depthByte map[int]int
}

func newStats() *stats {
	return &stats{nodes: map[string][]byte{}, depthCnt: map[int]int{}, depthByte: map[int]int{}}
}

// collectPath walks prefixes of keyNib under keyPrefix (raw addrHash for
// storage, empty for accounts) and records every existing trie node.
func (st *stats) collectPath(tx kv.Tx, table string, keyPrefix, keyNib []byte) {
	buf := make([]byte, 0, len(keyPrefix)+len(keyNib))
	for l := 0; l <= len(keyNib); l++ {
		buf = append(buf[:0], keyPrefix...)
		buf = append(buf, keyNib[:l]...)
		v, err := tx.GetOne(table, buf)
		if err != nil || v == nil {
			continue
		}
		st.naive += len(v)
		k := string(buf)
		if _, seen := st.nodes[k]; !seen {
			st.nodes[k] = append([]byte(nil), v...)
			depth := l // nibble depth within this trie
			st.depthCnt[depth]++
			st.depthByte[depth] += len(v)
		}
	}
}

func (st *stats) dedupBytes() int {
	t := 0
	for _, v := range st.nodes {
		t += len(v)
	}
	return t
}

func (st *stats) concat() []byte {
	keys := make([]string, 0, len(st.nodes))
	for k := range st.nodes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []byte
	for _, k := range keys {
		out = append(out, st.nodes[k]...)
	}
	return out
}

func report(name string, st *stats) {
	dedup := st.dedupBytes()
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	z := enc.EncodeAll(st.concat(), nil)
	enc.Close()
	fmt.Printf("\n=== %s ===\n", name)
	fmt.Printf("  unique nodes : %d\n", len(st.nodes))
	fmt.Printf("  naive        : %.2f MB (per-key, no sharing)\n", float64(st.naive)/1e6)
	fmt.Printf("  dedup(mproof): %.2f MB (%.1f%% of naive)\n", float64(dedup)/1e6, 100*float64(dedup)/float64(st.naive+1))
	fmt.Printf("  zstd         : %.2f MB (%.1f%% of dedup)\n", float64(len(z))/1e6, 100*float64(len(z))/float64(dedup+1))
	fmt.Printf("  per-depth (nibble depth: nodes, MB):\n")
	var upperBytes, lowerBytes int
	for d := 0; d <= 64; d++ {
		if c := st.depthCnt[d]; c > 0 {
			fmt.Printf("    d=%-2d : %7d nodes  %.2f MB\n", d, c, float64(st.depthByte[d])/1e6)
			if d <= 2 {
				upperBytes += st.depthByte[d]
			} else {
				lowerBytes += st.depthByte[d]
			}
		}
	}
	fmt.Printf("  upper(d<=2, ~每块必变,不可复用): %.3f MB\n", float64(upperBytes)/1e6)
	fmt.Printf("  lower(d>=3, unchanged 子树,连续时可复用): %.2f MB\n", float64(lowerBytes)/1e6)
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "N42 migrated trie chaindata")
	nAcct := flag.Int("accts", 600, "sampled changed accounts")
	nSlots := flag.Int("slots", 5, "sampled changed slots per contract")
	acctStep := flag.Int("acct-step", 100000, "sample every Nth HashedAccount")
	nStorAcct := flag.Int("stor-accts", 600, "sampled changed contracts (with storage)")
	storStep := flag.Int("stor-step", 5000, "sample every Nth distinct contract in HashedStorage")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	// --- sample accounts (hashedAddr) + their storage (hashedSlot) ---
	type acctSample struct {
		hashedAddr []byte
		slots      [][]byte // hashedSlot
	}
	var samples []acctSample
	ac, _ := tx.Cursor(tHashedAcc)
	i := 0
	for k, _, e := ac.First(); k != nil && len(samples) < *nAcct; k, _, e = ac.Next() {
		if e != nil {
			break
		}
		if len(k) != 32 {
			continue
		}
		if i%*acctStep == 0 {
			ha := append([]byte(nil), k...)
			// pull up to nSlots storage slots for this account
			var slots [][]byte
			sc, _ := tx.CursorDupSort(tHashedSto)
			for sv, se := sc.SeekBothRange(ha, nil); sv != nil && se == nil && len(slots) < *nSlots; _, sv, se = sc.NextDup() {
				if len(sv) >= 32 {
					slots = append(slots, append([]byte(nil), sv[:32]...))
				}
			}
			sc.Close()
			samples = append(samples, acctSample{hashedAddr: ha, slots: slots})
		}
		i++
	}
	ac.Close()

	totalSlots := 0
	for _, s := range samples {
		totalSlots += len(s.slots)
	}
	fmt.Printf("sampled %d accounts, %d storage slots (every %dth account, scanned %d)\n",
		len(samples), totalSlots, *acctStep, i)
	_ = crypto.Keccak256 // (kept: leaf hashing reference)

	// --- account multiproof: TrieAccount nodes on each keccak(addr) path ---
	accSt := newStats()
	for _, s := range samples {
		accSt.collectPath(tx, tTrieAcc, nil, toNibbles(s.hashedAddr))
	}

	// --- sample contracts WITH storage directly from HashedStorage ---
	var storSamples []acctSample
	{
		c, _ := tx.CursorDupSort(tHashedSto)
		j := 0
		for k, v, e := c.First(); k != nil && len(storSamples) < *nStorAcct; k, v, e = c.NextNoDup() {
			if e != nil {
				break
			}
			if len(k) != 32 {
				continue
			}
			if j%*storStep == 0 {
				ha := append([]byte(nil), k...)
				var slots [][]byte
				if len(v) >= 32 {
					slots = append(slots, append([]byte(nil), v[:32]...))
				}
				sc, _ := tx.CursorDupSort(tHashedSto)
				for sv, se := sc.SeekBothRange(ha, nil); sv != nil && se == nil && len(slots) < *nSlots; _, sv, se = sc.NextDup() {
					if len(sv) >= 32 {
						slots = append(slots, append([]byte(nil), sv[:32]...))
					}
				}
				sc.Close()
				storSamples = append(storSamples, acctSample{hashedAddr: ha, slots: slots})
			}
			j++
		}
		c.Close()
	}
	storTotal := 0
	for _, s := range storSamples {
		storTotal += len(s.slots)
	}
	fmt.Printf("sampled %d contracts, %d storage slots (every %dth distinct contract)\n",
		len(storSamples), storTotal, *storStep)

	// --- storage multiproof: TrieStorage nodes, key = addrHash(32 raw) + slotNibbles ---
	stoSt := newStats()
	for _, s := range storSamples {
		for _, slot := range s.slots {
			stoSt.collectPath(tx, tTrieSto, s.hashedAddr, toNibbles(slot))
		}
	}

	report("ACCOUNT multiproof", accSt)
	report("STORAGE multiproof", stoSt)

	// combined
	comb := newStats()
	for k, v := range accSt.nodes {
		comb.nodes["a"+k] = v
	}
	for k, v := range stoSt.nodes {
		comb.nodes["s"+k] = v
	}
	comb.naive = accSt.naive + stoSt.naive
	for d, c := range accSt.depthCnt {
		comb.depthCnt[d] += c
		comb.depthByte[d] += accSt.depthByte[d]
	}
	for d, c := range stoSt.depthCnt {
		comb.depthCnt[d] += c
		comb.depthByte[d] += stoSt.depthByte[d]
	}
	dedup := comb.dedupBytes()
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	z := enc.EncodeAll(comb.concat(), nil)
	enc.Close()
	fmt.Printf("\n=== COMBINED (account + storage) per block ===\n")
	fmt.Printf("  naive        : %.2f MB\n", float64(comb.naive)/1e6)
	fmt.Printf("  dedup(mproof): %.2f MB\n", float64(dedup)/1e6)
	fmt.Printf("  zstd         : %.2f MB (%.1f%% of dedup)\n", float64(len(z))/1e6, 100*float64(len(z))/float64(dedup+1))
	_ = bytes.Equal
}
