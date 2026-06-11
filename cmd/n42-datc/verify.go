// n42-datc verify — reconstructs the state root at sampled historical heights
// PURELY from the DATC records (epoch node bytes + change windows + leaf
// history) and compares each against the real header's stateRoot. This is the
// end-to-end gate for the temporal reconstruction: a single wrong child hash,
// window bound, or leaf floor breaks the root.
//
//	n42-datc verify --out D:/n42-datc-proto --headers D:/n42-eth1/chain/freezer \
//	  --samples 50 --fold-depth 4
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

var emptyTrieRoot = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

func runVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	out := fs.String("out", "", "DATC MDBX dir (from build)")
	hdrDir := fs.String("headers", `D:/n42-eth1/chain/freezer`, "headerc freezer dir")
	samples := fs.Int("samples", 50, "sampled historical heights")
	foldDepth := fs.Int("fold-depth", 4, "account-trie depth at/below which subtrees fold from leaf history")
	seed := fs.Int64("seed", 42, "sampling seed")
	mapGB := fs.Int("map.gb", 512, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	hdrs, err := ethel.OpenHeaderCompact(*hdrDir)
	if err != nil {
		die("open headerc: %v", err)
	}
	defer hdrs.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	// Load meta: head + schedule.
	metaV, err := tx.GetOne(tDatcMeta, []byte("head"))
	if err != nil || len(metaV) < 8 {
		die("DATC meta missing (run build first): %v", err)
	}
	head := binary.BigEndian.Uint64(metaV)
	schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
	var sched epochSchedule
	for d := 0; d <= maxChgDepth && (d+1)*8 <= len(schedV); d++ {
		sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
	}

	q := &querier{tx: tx, sched: sched, foldDepth: *foldDepth}
	rng := rand.New(rand.NewSource(*seed))

	fmt.Printf("DATC verify: head=%d samples=%d foldDepth=%d\n", head, *samples, *foldDepth)
	var lat []time.Duration
	okCnt := 0
	for i := 0; i < *samples; i++ {
		n := rng.Uint64() % head
		hdr, err := hdrs.ReadHeader(n)
		if err != nil {
			die("header %d: %v", n, err)
		}
		q.folds, q.recs, q.leafReads = 0, 0, 0
		t0 := time.Now()
		root, exists, err := q.nodeHashAt(nil, nil, n)
		el := time.Since(t0)
		if err != nil {
			die("rootAt(%d): %v", n, err)
		}
		if !exists {
			root = emptyTrieRoot
		}
		lat = append(lat, el)
		status := "OK "
		if root != hdr.Root {
			status = "FAIL"
		} else {
			okCnt++
		}
		fmt.Printf("  [%s] N=%-8d root=%x.. hdr=%x..  %6s  (recs=%d folds=%d leafReads=%d)\n",
			status, n, root[:6], hdr.Root[:6], el.Round(time.Millisecond), q.recs, q.folds, q.leafReads)
		if status == "FAIL" {
			die("root mismatch at block %d: datc=%x header=%x", n, root, hdr.Root)
		}
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	var sum time.Duration
	for _, l := range lat {
		sum += l
	}
	fmt.Printf("\n✅ %d/%d historical roots reconstructed byte-exact from DATC records.\n", okCnt, *samples)
	fmt.Printf("latency: avg=%s p50=%s p99=%s\n",
		(sum / time.Duration(len(lat))).Round(time.Millisecond),
		lat[len(lat)/2].Round(time.Millisecond),
		lat[len(lat)*99/100].Round(time.Millisecond))
}

// querier reconstructs node hashes at historical heights from DATC data.
type querier struct {
	tx        kv.Tx
	sched     epochSchedule
	foldDepth int

	folds, recs, leafReads int
}

// nodeHashAt returns the hash of the trie node at `path` (nibbles, relative to
// the domain) as of block N. domain nil = account trie; 40B addrHash+inc =
// that account's storage trie. exists=false → no subtree at this position.
//
// Record-driven path: the floor epoch record gives the children hashes at the
// epoch base; the change window tells which children changed since — those
// recurse. Falls back to a leaf-history fold (handles extensions, inline
// children, branch collapses natively) at/below foldDepth, when no record
// exists, or when the record shape is not a clean fully-hashed branch.
func (q *querier) nodeHashAt(domain, path []byte, n uint64) (types.Hash, bool, error) {
	d := len(path)
	fold := q.foldDepth
	if domain != nil {
		fold = 2 // storage tries are small; fold early
	}
	if d >= fold {
		return q.foldAt(domain, path, n)
	}
	rec, recEpoch, ok, err := q.floorRecord(domain, path, n)
	if err != nil {
		return types.Hash{}, false, err
	}
	if !ok || len(rec) == 0 {
		// Never a persisted branch up to N (or tombstone) — resolve from leaves.
		return q.foldAt(domain, path, n)
	}
	q.recs++
	hasState, _, hasHash, hashes, _ := trie.UnmarshalTrieNode(rec)
	if hasState != hasHash || hasState == 0 {
		// Mixed/inline children — the plain 17-RLP assembly below would be
		// wrong; the fold handles every shape correctly.
		return q.foldAt(domain, path, n)
	}

	// Children changed since the record: only N's own epoch can hold changes
	// (no record in a later epoch ⇒ untouched in it). If the record IS at N's
	// epoch, it is the end-of-epoch state: usable directly only when N is the
	// epoch's last block; otherwise step back and replay the window.
	curEpoch := q.sched.epochOf(d, n)
	eLen := q.sched.e[d]
	if recEpoch == curEpoch && (n+1)%eLen != 0 {
		rec2, recEpoch2, ok2, err2 := q.floorRecordBefore(domain, path, curEpoch)
		if err2 != nil {
			return types.Hash{}, false, err2
		}
		if !ok2 || len(rec2) == 0 {
			return q.foldAt(domain, path, n)
		}
		rec, recEpoch = rec2, recEpoch2
		hasState, _, hasHash, hashes, _ = trie.UnmarshalTrieNode(rec)
		if hasState != hasHash || hasState == 0 {
			return q.foldAt(domain, path, n)
		}
	}
	var changed map[byte]bool
	if recEpoch < curEpoch {
		changed, err = q.changedChildren(domain, path, curEpoch, n)
		if err != nil {
			return types.Hash{}, false, err
		}
	} // recEpoch == curEpoch with N at epoch end → no window

	// Assemble the branch: unchanged children from the record, changed ones
	// recursively at N.
	var slots [16]*types.Hash
	nKids := 0
	hashIdx := 0
	for nib := byte(0); nib < 16; nib++ {
		bit := uint16(1) << nib
		inBase := hasState&bit != 0
		var baseHash types.Hash
		if inBase {
			copy(baseHash[:], hashes[hashIdx*32:hashIdx*32+32])
			hashIdx++
		}
		if changed[nib] {
			h, exists, err := q.nodeHashAt(domain, append(append([]byte{}, path...), nib), n)
			if err != nil {
				return types.Hash{}, false, err
			}
			if exists {
				hc := h
				slots[nib] = &hc
				nKids++
			}
			continue
		}
		if inBase {
			hc := baseHash
			slots[nib] = &hc
			nKids++
		}
	}
	if nKids == 0 {
		return types.Hash{}, false, nil
	}
	if nKids == 1 {
		// Branch collapsed at N — the node is a leaf/extension now; only the
		// fold knows its true shape.
		return q.foldAt(domain, path, n)
	}
	// keccak(RLP 17-list: 16 child slots + empty value)
	return branch17Hash(slots), true, nil
}

// branch17Hash hashes a full branch node whose present children are all
// 32-byte hash references: keccak(RLP([c0..c15, ""])).
func branch17Hash(slots [16]*types.Hash) types.Hash {
	payload := 0
	for _, s := range slots {
		if s != nil {
			payload += 33
		} else {
			payload++
		}
	}
	payload++ // empty value slot
	var buf bytes.Buffer
	if payload < 56 {
		buf.WriteByte(byte(0xc0 + payload))
	} else {
		var be [8]byte
		binary.BigEndian.PutUint64(be[:], uint64(payload))
		i := 0
		for be[i] == 0 {
			i++
		}
		buf.WriteByte(byte(0xf7 + (8 - i)))
		buf.Write(be[i:])
	}
	for _, s := range slots {
		if s != nil {
			buf.WriteByte(0xa0)
			buf.Write(s[:])
		} else {
			buf.WriteByte(0x80)
		}
	}
	buf.WriteByte(0x80)
	h := sha3.NewLegacyKeccak256()
	h.Write(buf.Bytes())
	var out types.Hash
	h.Sum(out[:0])
	return out
}

// floorRecord returns the newest node record with epoch ≤ epochOf(d, n).
func (q *querier) floorRecord(domain, path []byte, n uint64) ([]byte, uint64, bool, error) {
	d := len(path)
	return q.floorRecordBefore(domain, path, q.sched.epochOf(d, n)+1)
}

// floorRecordBefore returns the newest record with epoch < beforeEpoch.
func (q *querier) floorRecordBefore(domain, path []byte, beforeEpoch uint64) ([]byte, uint64, bool, error) {
	table := tDatcAccNode
	full := path
	if domain != nil {
		table = tDatcStoNode
		full = append(append([]byte{}, domain...), path...)
	}
	prefix := make([]byte, 0, 1+len(full))
	prefix = append(prefix, byte(len(full)))
	prefix = append(prefix, full...)

	c, err := q.tx.Cursor(table)
	if err != nil {
		return nil, 0, false, err
	}
	defer c.Close()
	seek := append(append([]byte{}, prefix...), 0, 0, 0, 0)
	binary.BigEndian.PutUint32(seek[len(prefix):], uint32(beforeEpoch))
	k, v, err := c.Seek(seek)
	if err != nil {
		return nil, 0, false, err
	}
	// Step back to the last key strictly below `seek`.
	if k == nil {
		k, v, err = c.Last()
	} else {
		k, v, err = c.Prev()
	}
	if err != nil || k == nil {
		return nil, 0, false, err
	}
	if len(k) != len(prefix)+4 || !bytes.HasPrefix(k, prefix) {
		return nil, 0, false, nil
	}
	epoch := uint64(binary.BigEndian.Uint32(k[len(prefix):]))
	return append([]byte{}, v...), epoch, true, nil
}

// changedChildren scans the change window (epochStart..n] of n's epoch.
func (q *querier) changedChildren(domain, path []byte, epoch, n uint64) (map[byte]bool, error) {
	d := len(path)
	table := tDatcAccChg
	if domain != nil {
		table = tDatcStoChg
	}
	prefix := make([]byte, 0, 1+len(domain)+d+4)
	prefix = append(prefix, byte(d))
	prefix = append(prefix, domain...)
	prefix = append(prefix, path...)
	prefix = binary.BigEndian.AppendUint32(prefix, uint32(epoch))

	c, err := q.tx.Cursor(table)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	out := make(map[byte]bool)
	for k, _, err := c.Seek(prefix); k != nil; k, _, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if len(k) != len(prefix)+5 {
			continue
		}
		blk := uint64(binary.BigEndian.Uint32(k[len(prefix):]))
		if blk > n {
			break // keys sort by block within the epoch
		}
		out[k[len(prefix)+4]] = true
	}
	return out, nil
}

// foldAt rebuilds the subtree at `path` from the leaf history as of block N
// and returns its embedded hash (handles extensions/leaves/inline natively via
// GenStructStep — the same machinery production loaders use).
func (q *querier) foldAt(domain, path []byte, n uint64) (types.Hash, bool, error) {
	q.folds++
	leaves, err := q.asOfLeaves(domain, path, n)
	if err != nil {
		return types.Hash{}, false, err
	}
	if len(leaves) == 0 {
		return types.Hash{}, false, nil
	}
	hb := trie.NewHashBuilder(false)
	var groups, hasTreeA, hasHashA []uint16
	var curr, succ []byte
	var currVal trie.GenStructStepLeafData
	retain := func(_ []byte) bool { return false }
	hc := func([]byte, uint16, uint16, uint16, []byte, []byte) error { return nil }

	for i := range leaves {
		succ = append(succ[:0], leaves[i].remainder...)
		if len(curr) > 0 {
			groups, hasTreeA, hasHashA, err = trie.GenStructStep(retain, curr, succ, hb, hc, &currVal, groups, hasTreeA, hasHashA, false)
			if err != nil {
				return types.Hash{}, false, err
			}
		}
		curr = append(curr[:0], succ...)
		currVal.Value = leaves[i].value
	}
	if _, _, _, err := trie.GenStructStep(retain, curr, []byte{}, hb, hc, &currVal, groups, hasTreeA, hasHashA, false); err != nil {
		return types.Hash{}, false, err
	}
	root, err := hb.RootHash()
	if err != nil {
		return types.Hash{}, false, err
	}
	return root, true, nil
}

type foldLeaf struct {
	remainder []byte // nibbles below `path`
	value     rlphacks.RlpSerializable
}

// asOfLeaves enumerates the leaves under `path` as of block N from the leaf
// history: per key, the floor entry ≤ N is its value (empty = absent).
func (q *querier) asOfLeaves(domain, path []byte, n uint64) ([]foldLeaf, error) {
	table := tDatcLeafA
	keyLen := 32
	if domain != nil {
		table = tDatcLeafS
		keyLen = 72
	}
	// Byte-prefix from the nibble path (odd nibble → filter below).
	fullNibbles := path
	if domain != nil {
		// storage leaf keys are domain(40B raw) + slotHash(32B); path nibbles
		// address the slotHash part.
		fullNibbles = path
	}
	bytePrefix := make([]byte, 0, len(domain)+len(fullNibbles)/2)
	bytePrefix = append(bytePrefix, domain...)
	for i := 0; i+1 < len(fullNibbles); i += 2 {
		bytePrefix = append(bytePrefix, fullNibbles[i]<<4|fullNibbles[i+1])
	}
	oddNib := len(fullNibbles)%2 == 1
	var oddVal byte
	if oddNib {
		oddVal = fullNibbles[len(fullNibbles)-1]
	}

	c, err := q.tx.Cursor(table)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var out []foldLeaf
	var curKey []byte
	var curVal []byte
	var haveFloor bool
	flush := func() error {
		if curKey == nil || !haveFloor || len(curVal) == 0 {
			return nil
		}
		nibs := nibblesOf(curKey[len(domain):]) // hashed key part
		rem := append([]byte{}, nibs[len(fullNibbles):]...)
		var val rlphacks.RlpSerializable
		if domain == nil {
			var acct account.StateAccount
			if err := acct.DecodeForStorage(curVal); err != nil {
				return fmt.Errorf("leaf account decode: %w", err)
			}
			// storage root at N for this account (emptyRoot for EOAs).
			var addrHash types.Hash
			copy(addrHash[:], curKey[:32])
			sd := make([]byte, 40)
			copy(sd, addrHash[:])
			sroot, hasStorage, err := q.nodeHashAt(sd, nil, n)
			if err != nil {
				return err
			}
			if hasStorage {
				acct.Root = sroot
			} else {
				acct.Root = emptyTrieRoot
			}
			buf := make([]byte, acct.EncodingLengthForHashing())
			acct.EncodeForHashing(buf)
			val = rlphacks.RlpEncodedBytes(buf)
		} else {
			val = rlphacks.RlpSerializableBytes(append([]byte{}, curVal...))
		}
		out = append(out, foldLeaf{remainder: rem, value: val})
		return nil
	}

	for k, v, err := c.Seek(bytePrefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, bytePrefix) {
			break
		}
		if len(k) != keyLen+8 {
			continue
		}
		hk := k[:keyLen]
		if oddNib {
			nib := hk[len(bytePrefix)] >> 4
			if nib != oddVal {
				continue
			}
		}
		blk := binary.BigEndian.Uint64(k[keyLen:])
		if !bytes.Equal(hk, curKey) {
			if err := flush(); err != nil {
				return nil, err
			}
			curKey = append([]byte{}, hk...)
			curVal, haveFloor = nil, false
		}
		if blk <= n {
			curVal = append(curVal[:0], v...)
			haveFloor = true
			q.leafReads++
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}
