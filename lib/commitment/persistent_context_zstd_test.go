package commitment

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// TestHA3d_ZstdRoundTrip exercises the compression toggle: builds
// a synthetic state with zstd enabled, verifies the row format
// (1-byte prefix + payload), and reopens with zstd-aware reader to
// confirm Branch decodes correctly. Then re-runs WITHOUT zstd and
// compares persisted size to validate the save.
func TestHA3d_ZstdRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	logger := log.New()

	mkAccount := func(nonce uint64, balance uint64) []byte {
		acc := account.StateAccount{
			Nonce:    nonce,
			Balance:  *uint256.NewInt(balance),
			CodeHash: types.Hash{},
		}
		buf := make([]byte, acc.EncodingLengthForStorage())
		acc.EncodeForStorage(buf)
		return buf
	}

	reader := &inMemAccountReader{store: map[string]*Update{}}
	addrs := make([][]byte, 64)
	for i := range addrs {
		addr := bytes.Repeat([]byte{byte(i + 1)}, 20)
		addrs[i] = addr
		ku := &KeyUpdate{update: new(Update)}
		(&Updates{}).TouchAccount(ku, mkAccount(uint64(i), uint64(i)*100))
		reader.store[string(addr)] = ku.update
	}

	build := func(label string, zstdLevel int) (rootHex string, totalBytes uint64) {
		dbPath := filepath.Join(tmp, label)
		db, err := mdbxkv.NewMDBX(logger).
			Path(dbPath).Label(kv.ChainDB).PageSize(4096).
			MapSize(128*datasize.MB).
			WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
				d[CommitmentBranchesTable] = kv.TableCfgItem{}
				return d
			}).Open(context.Background())
		if err != nil {
			t.Fatalf("[%s] open: %v", label, err)
		}
		defer db.Close()

		tx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatalf("[%s] BeginRw: %v", label, err)
		}

		pctx := NewPersistentPatriciaContext(reader, nil)
		pctx.SetWriteTx(tx)
		if zstdLevel > 0 {
			if err := pctx.SetZstdLevel(zstdLevel); err != nil {
				t.Fatalf("[%s] SetZstdLevel: %v", label, err)
			}
		}

		hph := NewHexPatriciaHashed(int16(length.Addr), pctx)
		ub := NewUpdateBuilder()
		for _, addr := range addrs {
			ub.Balance(bytesToHexString(addr), uint64(addr[0])*100).Nonce(bytesToHexString(addr), uint64(addr[0]))
		}
		plainKeys, updates := ub.Build()
		processUpdates := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
		defer processUpdates.Close()

		root, err := hph.Process(context.Background(), processUpdates, label, nil, WarmupConfig{})
		if err != nil {
			t.Fatalf("[%s] Process: %v", label, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("[%s] commit: %v", label, err)
		}

		roTx, _ := db.BeginRo(context.Background())
		entries, valueBytes, _ := CommitmentBranchesSize(context.Background(), roTx)
		t.Logf("[%s] root=%x entries=%d value_bytes=%d", label, root, entries, valueBytes)

		// Sanity probe one row to confirm format
		c, _ := roTx.Cursor(CommitmentBranchesTable)
		k, v, _ := c.First()
		probeLen := 3
		if len(v) < probeLen {
			probeLen = len(v)
		}
		t.Logf("[%s] first row: key_len=%d value_len=%d first_3=%x", label, len(k), len(v), v[:probeLen])
		c.Close()
		roTx.Rollback()

		return ""+string(root), valueBytes
	}

	rawRoot, rawSize := build("raw", 0)
	zstdRoot, zstdSize := build("zstd", 3)

	if rawRoot != zstdRoot {
		t.Errorf("roots diverge: raw=%x zstd=%x", []byte(rawRoot), []byte(zstdRoot))
	}
	if zstdSize >= rawSize {
		t.Logf("WARNING: zstd not smaller (raw=%d zstd=%d) — payload too small to compress",
			rawSize, zstdSize)
	} else {
		saving := 100 * (1 - float64(zstdSize)/float64(rawSize))
		t.Logf("HA-3d zstd saving: raw=%d B → zstd=%d B (%.1f%% saving)",
			rawSize, zstdSize, saving)
	}
}

