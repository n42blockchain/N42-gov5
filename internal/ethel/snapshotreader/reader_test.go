package snapshotreader

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

// writeMiniTable writes a snapshot table (<prefix>.idx/.ef/.val) in the exact
// format cmd/reth-snapshot-export produces: RecSplit MPHF over the keys, .val
// entries [1B len][4B fp BE][value] in ordinal order, EF mapping ordinal->offset.
func writeMiniTable(t *testing.T, dir, prefix string, kvs [][2][]byte) {
	t.Helper()
	idxPath := filepath.Join(dir, prefix+".idx")
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   len(kvs),
		BucketSize: 2000,
		IndexFile:  idxPath,
		TmpDir:     dir,
		LeafSize:   8,
		NoValues:   true,
	}, log.New())
	require.NoError(t, err)
	feed := func() {
		for i, kv := range kvs {
			require.NoError(t, rs.AddKey(kv[0], uint64(i)))
		}
	}
	feed()
	for {
		err := rs.Build(context.Background())
		if err == nil {
			break
		}
		require.True(t, rs.Collision(), "recsplit build: %v", err)
		rs.ResetNextSalt()
		feed()
	}

	idx, err := recsplit.OpenIndex(idxPath)
	require.NoError(t, err)
	defer idx.Close()
	rd := recsplit.NewIndexReader(idx)

	type op struct {
		ord     uint64
		payload []byte
	}
	ops := make([]op, 0, len(kvs))
	for _, kv := range kvs {
		ord, ok := rd.Lookup(kv[0])
		require.True(t, ok)
		fp := uint32(xxhash.Sum64(kv[0]))
		payload := []byte{byte(fp >> 24), byte(fp >> 16), byte(fp >> 8), byte(fp)}
		payload = append(payload, kv[1]...)
		ops = append(ops, op{ord, payload})
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].ord < ops[j].ord })

	ef := eliasfano32.NewEliasFano(uint64(len(ops)), uint64(len(ops))*64)
	var valBuf []byte
	var off uint64
	for _, o := range ops {
		ef.AddOffset(off)
		valBuf = append(valBuf, byte(len(o.payload)))
		valBuf = append(valBuf, o.payload...)
		off += 1 + uint64(len(o.payload))
	}
	ef.Build()
	require.NoError(t, os.WriteFile(filepath.Join(dir, prefix+".val"), valBuf, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, prefix+".ef"), ef.AppendBytes(nil), 0644))
}

func writeCodeDict(t *testing.T, dir, accPrefix string, hashes [][32]byte) {
	t.Helper()
	buf := make([]byte, 4)
	binary.LittleEndian.PutUint32(buf, uint32(len(hashes)))
	for _, h := range hashes {
		buf = append(buf, h[:]...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, accPrefix+".codedict"), buf, 0644))
}

func TestSnapshotReaderRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// accounts: 6 addrs with distinct values
	type acctKV struct {
		addr [20]byte
		val  []byte
	}
	var accts []acctKV
	var accKVs [][2][]byte
	for i := 0; i < 6; i++ {
		var a [20]byte
		a[0] = byte(i * 7)
		a[19] = byte(i + 1)
		val := []byte{byte(i), 0x11, 0x22, byte(0xa0 + i)}
		accts = append(accts, acctKV{a, val})
		k := make([]byte, 20)
		copy(k, a[:])
		accKVs = append(accKVs, [2][]byte{k, val})
	}
	writeMiniTable(t, dir, "accounts", accKVs)
	writeCodeDict(t, dir, "accounts", [][32]byte{
		{0xaa, 0x01}, {0xbb, 0x02}, {0xcc, 0x03},
	})

	// storage: 10 addr+slot with distinct U256 values
	type stoKV struct {
		addr [20]byte
		slot [32]byte
		val  []byte
	}
	var stors []stoKV
	var stoKVs [][2][]byte
	for i := 0; i < 10; i++ {
		var a [20]byte
		a[0] = byte((i % 3) * 7) // reuse some addrs
		a[19] = byte((i % 3) + 1)
		var s [32]byte
		s[0] = byte(i)
		s[31] = byte(i*13 + 1)
		val := []byte{byte(i + 1), byte(0xf0 - i)} // trimmed U256
		stors = append(stors, stoKV{a, s, val})
		k := make([]byte, 52)
		copy(k[:20], a[:])
		copy(k[20:], s[:])
		stoKVs = append(stoKVs, [2][]byte{k, val})
	}
	writeMiniTable(t, dir, "storage", stoKVs)

	seg, err := OpenSegment(dir, "accounts", "storage")
	require.NoError(t, err)
	defer seg.Close()

	// account values round-trip
	for _, a := range accts {
		got, ok := seg.AccountValueRaw(a.addr)
		require.True(t, ok, "account %x missing", a.addr[:4])
		require.Equal(t, a.val, got, "account %x value", a.addr[:4])
	}
	// storage values round-trip
	for _, s := range stors {
		got, ok := seg.StorageValue(s.addr, s.slot)
		require.True(t, ok, "storage %x/%x missing", s.addr[:4], s.slot[:4])
		require.Equal(t, s.val, got, "storage %x/%x value", s.addr[:4], s.slot[:4])
	}
	// codedict resolves
	h, ok := seg.CodeHashByID(0)
	require.True(t, ok)
	require.Equal(t, byte(0xaa), h[0])
	h, ok = seg.CodeHashByID(2)
	require.True(t, ok)
	require.Equal(t, byte(0xcc), h[0])
	_, ok = seg.CodeHashByID(99)
	require.False(t, ok)
	require.Equal(t, 3, seg.CodeDictLen())

	// phantom account (not present) must be rejected by the fingerprint guard
	var ghost [20]byte
	ghost[0] = 0xff
	ghost[19] = 0xfe
	_, ok = seg.AccountValueRaw(ghost)
	require.False(t, ok, "phantom account must miss")

	// phantom storage
	var ga [20]byte
	ga[0] = 0xff
	var gs [32]byte
	gs[0] = 0xee
	_, ok = seg.StorageValue(ga, gs)
	require.False(t, ok, "phantom storage must miss")
}

// writeMiniTableV2 writes a v2 sharded snapshot table (<prefix>.sNN.{idx,ef,val})
// exactly as cmd/reth-snapshot-export does: rows are routed to shards by the top
// log2(n) bits of xxhash64(key); each shard builds a NoValues MPHF (Lookup→slot),
// writes its [len:1B][fp:4B][value] entries in SLOT order, and an Elias-Fano
// mapping slot→byte offset into that .val.
func writeMiniTableV2(t *testing.T, dir, prefix string, kvs [][2][]byte, nShards int) {
	t.Helper()
	shift := uint(64 - bits.TrailingZeros(uint(nShards)))
	shards := make([][][2][]byte, nShards)
	for _, kv := range kvs {
		h := xxhash.Sum64(kv[0])
		shards[h>>shift] = append(shards[h>>shift], kv)
	}
	for si := range shards {
		rows := shards[si]
		base := filepath.Join(dir, fmt.Sprintf("%s.s%02d", prefix, si))
		rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
			KeyCount:   len(rows),
			BucketSize: 2000,
			IndexFile:  base + ".idx",
			TmpDir:     dir,
			LeafSize:   8,
			NoValues:   true,
		}, log.New())
		require.NoError(t, err)
		feed := func() {
			for _, kv := range rows {
				require.NoError(t, rs.AddKey(kv[0], 0))
			}
		}
		feed()
		for {
			err := rs.Build(context.Background())
			if err == nil {
				break
			}
			require.True(t, rs.Collision(), "recsplit build: %v", err)
			rs.ResetNextSalt()
			feed()
		}
		rs.Close()

		if len(rows) == 0 {
			require.NoError(t, os.WriteFile(base+".val", nil, 0644))
			require.NoError(t, os.WriteFile(base+".ef", nil, 0644))
			continue
		}
		idx, err := recsplit.OpenIndex(base + ".idx")
		require.NoError(t, err)
		rd := recsplit.NewIndexReader(idx)
		// place each row's payload at its MPHF slot, then serialize in slot order
		payloads := make([][]byte, len(rows))
		for _, kv := range rows {
			slot, ok := rd.Lookup(kv[0])
			require.True(t, ok)
			fp := uint32(xxhash.Sum64(kv[0]))
			p := []byte{byte(4 + len(kv[1])), byte(fp >> 24), byte(fp >> 16), byte(fp >> 8), byte(fp)}
			payloads[slot] = append(p, kv[1]...)
		}
		idx.Close()
		var val []byte
		ef := eliasfano32.NewEliasFano(uint64(len(rows)), uint64(len(rows))*64)
		for _, p := range payloads {
			require.NotNil(t, p, "slot gap")
			ef.AddOffset(uint64(len(val)))
			val = append(val, p...)
		}
		ef.Build()
		require.NoError(t, os.WriteFile(base+".val", val, 0644))
		require.NoError(t, os.WriteFile(base+".ef", ef.AppendBytes(nil), 0644))
	}
}

// TestSnapshotReaderV2RoundTrip exercises the sharded Enums layout end to end:
// values round-trip through shard routing + embedded-EF offsets, empty shards
// (4 shards, few keys) open and miss cleanly, and phantom keys are rejected by
// the fingerprint.
func TestSnapshotReaderV2RoundTrip(t *testing.T) {
	dir := t.TempDir()
	const nShards = 4

	var accts [][2][]byte
	for i := 0; i < 6; i++ {
		k := make([]byte, 20)
		k[0] = byte(i * 7)
		k[19] = byte(i + 1)
		accts = append(accts, [2][]byte{k, {byte(i), 0x11, 0x22, byte(0xa0 + i)}})
	}
	writeMiniTableV2(t, dir, "accounts", accts, nShards)
	writeCodeDict(t, dir, "accounts", [][32]byte{{0xaa, 0x01}, {0xbb, 0x02}})

	var stors [][2][]byte
	for i := 0; i < 10; i++ {
		k := make([]byte, 52)
		k[0] = byte((i % 3) * 7)
		k[19] = byte((i % 3) + 1)
		k[20] = byte(i)
		k[51] = byte(i*13 + 1)
		stors = append(stors, [2][]byte{k, {byte(i + 1), byte(0xf0 - i)}})
	}
	writeMiniTableV2(t, dir, "storage", stors, nShards)

	seg, err := OpenSegment(dir, "accounts", "storage")
	require.NoError(t, err)
	defer seg.Close()
	require.Len(t, seg.acc.shards, nShards)
	require.Len(t, seg.sto.shards, nShards)

	for _, kv := range accts {
		var addr [20]byte
		copy(addr[:], kv[0])
		got, ok := seg.AccountValueRaw(addr)
		require.True(t, ok, "account %x missing", addr[:4])
		require.Equal(t, kv[1], got)
	}
	for _, kv := range stors {
		var addr [20]byte
		var slot [32]byte
		copy(addr[:], kv[0][:20])
		copy(slot[:], kv[0][20:])
		got, ok := seg.StorageValue(addr, slot)
		require.True(t, ok, "storage %x/%x missing", addr[:4], slot[:4])
		require.Equal(t, kv[1], got)
	}

	// phantom keys must miss in every shard (fp guard behind shard routing)
	for i := 0; i < 32; i++ {
		var ghost [20]byte
		ghost[0] = byte(0xf0 + i)
		ghost[19] = byte(0xe0 - i)
		_, ok := seg.AccountValueRaw(ghost)
		require.False(t, ok, "phantom account %x must miss", ghost[:2])
	}
	var ga [20]byte
	ga[0] = 0xff
	var gs [32]byte
	gs[0] = 0xee
	_, ok := seg.StorageValue(ga, gs)
	require.False(t, ok, "phantom storage must miss")
}

// TestSnapshotReaderV2EmptyShards pins the empty-shard edge: with one key and
// four shards, three shards hold zero keys — they must build, open, and miss
// cleanly (including phantom lookups routed into them).
func TestSnapshotReaderV2EmptyShards(t *testing.T) {
	dir := t.TempDir()
	k := make([]byte, 20)
	k[0], k[19] = 0x42, 0x01
	writeMiniTableV2(t, dir, "accounts", [][2][]byte{{k, {0xde, 0xad}}}, 4)
	writeCodeDict(t, dir, "accounts", [][32]byte{{0xaa}})
	sk := make([]byte, 52)
	sk[0], sk[51] = 0x42, 0x02
	writeMiniTableV2(t, dir, "storage", [][2][]byte{{sk, {0xbe, 0xef}}}, 4)

	seg, err := OpenSegment(dir, "accounts", "storage")
	require.NoError(t, err)
	defer seg.Close()

	empty := 0
	for _, sh := range seg.acc.shards {
		if sh.idx.KeyCount() == 0 {
			empty++
		}
	}
	require.Equal(t, 3, empty, "expected exactly 3 empty account shards")

	var addr [20]byte
	copy(addr[:], k)
	got, ok := seg.AccountValueRaw(addr)
	require.True(t, ok)
	require.Equal(t, []byte{0xde, 0xad}, got)

	// sweep phantoms so every shard (incl. empty ones) sees at least one miss
	for i := 0; i < 64; i++ {
		var ghost [20]byte
		ghost[0], ghost[19] = byte(i), byte(0x80 + i)
		if ghost == addr {
			continue
		}
		_, ok := seg.AccountValueRaw(ghost)
		require.False(t, ok, "phantom %x must miss", ghost[:2])
	}
}

// TestReadMaybeZstd covers the .val/.zst precedence rules: an existing .val is
// used as-is (a sibling .zst — even a corrupt one — must never be touched), and
// a missing .val is restored by streaming the .zst to disk, not into the heap.
func TestReadMaybeZstd(t *testing.T) {
	dir := t.TempDir()
	valPath := filepath.Join(dir, "storage.val")
	payload := []byte("snapshot-val-payload-0123456789")

	// .val present + garbage .zst sibling: the .zst must be ignored.
	require.NoError(t, os.WriteFile(valPath, payload, 0644))
	require.NoError(t, os.WriteFile(valPath+".zst", []byte("not zstd at all"), 0644))
	data, f, m2, err := readMaybeZstd(valPath)
	require.NoError(t, err)
	require.Equal(t, payload, []byte(data))
	closeMapping(data, f, m2)

	// .val missing + valid .zst: decompressed to disk, then served.
	require.NoError(t, os.Remove(valPath))
	enc, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	compressed := enc.EncodeAll(payload, nil)
	require.NoError(t, enc.Close())
	require.NoError(t, os.WriteFile(valPath+".zst", compressed, 0644))
	data, f, m2, err = readMaybeZstd(valPath)
	require.NoError(t, err)
	require.Equal(t, payload, []byte(data))
	closeMapping(data, f, m2)
	st, err := os.Stat(valPath)
	require.NoError(t, err, ".val must be materialized on disk")
	require.Equal(t, int64(len(payload)), st.Size())
	_, err = os.Stat(valPath + ".tmp")
	require.True(t, os.IsNotExist(err), "temp file must not linger")

	// neither file: error surfaces.
	require.NoError(t, os.Remove(valPath))
	require.NoError(t, os.Remove(valPath+".zst"))
	_, _, _, err = readMaybeZstd(valPath)
	require.Error(t, err)
}

// snapAcctValue builds a snapshot account value: V2-compact, with a contract's
// trailing 32B codeHash replaced by a 3-byte codedict id (codeHashID>=0). For
// an EOA (codeHashID<0) the value is plain V2 (no codeHash field).
func snapAcctValue(nonce uint64, bal *uint256.Int, codeHash *types.Hash, codeHashID int) []byte {
	a := &account.StateAccount{Nonce: nonce}
	a.Balance.Set(bal)
	if codeHash != nil {
		a.CodeHash = *codeHash
	}
	full := a.MarshalV2()
	if codeHashID >= 0 {
		prefix := full[:len(full)-32] // strip 32B codeHash
		return append(append([]byte(nil), prefix...),
			byte(codeHashID>>16), byte(codeHashID>>8), byte(codeHashID))
	}
	return full
}

func TestSnapshotReaderDecodeAccount(t *testing.T) {
	dir := t.TempDir()

	dict := [][32]byte{{0xaa, 0x01}, {0xbb, 0x02}, {0xcc, 0x03}}
	var ch1 types.Hash
	copy(ch1[:], dict[1][:]) // codedict id=1

	var caddr [20]byte // contract
	caddr[0] = 1
	cval := snapAcctValue(5, uint256.NewInt(1000), &ch1, 1)

	var eaddr [20]byte // EOA
	eaddr[0] = 2
	eval := snapAcctValue(2, uint256.NewInt(500), nil, -1)

	writeMiniTable(t, dir, "accounts", [][2][]byte{{caddr[:], cval}, {eaddr[:], eval}})
	writeCodeDict(t, dir, "accounts", dict)
	// storage table needs >=1 entry for RecSplit
	dummyKey := make([]byte, 52)
	dummyKey[0] = 9
	writeMiniTable(t, dir, "storage", [][2][]byte{{dummyKey, {0x01}}})

	seg, err := OpenSegment(dir, "accounts", "storage")
	require.NoError(t, err)
	defer seg.Close()

	// contract: codeHash restored from 3B id via codedict
	raw, ok := seg.AccountValueRaw(caddr)
	require.True(t, ok)
	a, err := seg.DecodeAccount(raw)
	require.NoError(t, err)
	require.Equal(t, uint64(5), a.Nonce)
	require.Equal(t, uint64(1000), a.Balance.Uint64())
	require.Equal(t, ch1, a.CodeHash, "contract codeHash restored from dict")

	// EOA: empty codeHash
	raw, ok = seg.AccountValueRaw(eaddr)
	require.True(t, ok)
	a, err = seg.DecodeAccount(raw)
	require.NoError(t, err)
	require.Equal(t, uint64(2), a.Nonce)
	require.Equal(t, uint64(500), a.Balance.Uint64())
	require.True(t, account.IsEmptyCodeHash(a.CodeHash), "EOA must have empty codeHash")
}
