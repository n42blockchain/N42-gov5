package snapshotreader

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/cespare/xxhash/v2"
	"github.com/holiman/uint256"
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
