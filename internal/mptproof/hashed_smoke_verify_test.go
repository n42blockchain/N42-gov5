package mptproof

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// TestHashedIndex_SmokeDataPresent reads back a smoke-test build of
// the hashed index and verifies (1) both tables are populated, (2)
// keys are ascending and well-formed (32 B / 64 B), (3) the storage
// reference value is exactly 52 bytes.
//
// Set N42_HASHED_SMOKE_DIR=<dir> to point at a smoke output (e.g.
// the temp dir created by `n42-mpt-hashedindex --max-rows 1000`).
// Skips if the env var is empty.
func TestHashedIndex_SmokeDataPresent(t *testing.T) {
	dir := os.Getenv("N42_HASHED_SMOKE_DIR")
	if dir == "" {
		t.Skip("N42_HASHED_SMOKE_DIR not set; smoke verification skipped")
	}

	db, err := mdbxkv.NewMDBX(log.New()).
		Path(dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(2 * datasize.GB).Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[HashedAccountTable] = kv.TableCfgItem{}
			d[HashedStorageRefTable] = kv.TableCfgItem{}
			return d
		}).Open(context.Background())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	for _, c := range []struct {
		table      string
		wantKeyLen int
		wantValLen int // 0 = any
	}{
		{HashedAccountTable, 32, 0},
		{HashedStorageRefTable, 64, 52},
	} {
		cur, _ := tx.Cursor(c.table)
		var (
			rows       int
			prev       []byte
			ascending  = true
			firstK     []byte
			lastK      []byte
			badKeyLen  int
			badValLen  int
		)
		for k, v, err := cur.First(); err == nil && k != nil; k, v, err = cur.Next() {
			if firstK == nil {
				firstK = append([]byte{}, k...)
			}
			lastK = k
			if len(k) != c.wantKeyLen {
				badKeyLen++
			}
			if c.wantValLen > 0 && len(v) != c.wantValLen {
				badValLen++
			}
			if prev != nil && bytes.Compare(prev, k) >= 0 {
				ascending = false
			}
			prev = append(prev[:0], k...)
			rows++
		}
		cur.Close()
		t.Logf("%s rows=%d firstK=%x lastK=%x ascending=%v badKeyLen=%d badValLen=%d",
			c.table, rows, firstK[:8], lastK[:8], ascending, badKeyLen, badValLen)
		if rows == 0 {
			t.Errorf("%s: empty", c.table)
		}
		if !ascending {
			t.Errorf("%s: not ascending", c.table)
		}
		if badKeyLen != 0 {
			t.Errorf("%s: %d rows with wrong key length (want %d)", c.table, badKeyLen, c.wantKeyLen)
		}
		if badValLen != 0 {
			t.Errorf("%s: %d rows with wrong value length (want %d)", c.table, badValLen, c.wantValLen)
		}
	}
}
