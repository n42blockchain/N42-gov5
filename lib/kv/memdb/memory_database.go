/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package memdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func New(tmpDir string) kv.RwDB {
	return mdbx.NewMDBX(log.New()).InMem(tmpDir).MustOpen()
}

func NewPoolDB(tmpDir string) kv.RwDB {
	return mdbx.NewMDBX(log.New()).InMem(tmpDir).Label(kv.TxPoolDB).WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.TxpoolTablesCfg }).MustOpen()
}
func NewDownloaderDB(tmpDir string) kv.RwDB {
	return mdbx.NewMDBX(log.New()).InMem(tmpDir).Label(kv.DownloaderDB).WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.DownloaderTablesCfg }).MustOpen()
}
func NewSentryDB(tmpDir string) kv.RwDB {
	return mdbx.NewMDBX(log.New()).InMem(tmpDir).Label(kv.SentryDB).WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.SentryTablesCfg }).MustOpen()
}

// newTestDB creates a test database with cleanup registered on tb.
func newTestDB(tb testing.TB, open func(string) kv.RwDB) kv.RwDB {
	tb.Helper()
	db := open(tb.TempDir())
	tb.Cleanup(db.Close)
	return db
}

// newTestTx creates a test database and begins an RwTx, both with cleanup registered on tb.
func newTestTx(tb testing.TB, db kv.RwDB) (kv.RwDB, kv.RwTx) {
	tb.Helper()
	tx, err := db.BeginRw(context.Background()) //nolint:gocritic
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(tx.Rollback)
	return db, tx
}

func NewTestDB(tb testing.TB) kv.RwDB {
	tb.Helper()
	return newTestDB(tb, New)
}

func BeginRw(tb testing.TB, db kv.RwDB) kv.RwTx {
	tb.Helper()
	tx, err := db.BeginRw(context.Background()) //nolint:gocritic
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(tx.Rollback)
	return tx
}

func BeginRo(tb testing.TB, db kv.RoDB) kv.Tx {
	tb.Helper()
	tx, err := db.BeginRo(context.Background()) //nolint:gocritic
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(tx.Rollback)
	return tx
}

func NewTestPoolDB(tb testing.TB) kv.RwDB {
	tb.Helper()
	return newTestDB(tb, NewPoolDB)
}

func NewTestDownloaderDB(tb testing.TB) kv.RwDB {
	tb.Helper()
	return newTestDB(tb, NewDownloaderDB)
}

func NewTestSentryDB(tb testing.TB) kv.RwDB {
	tb.Helper()
	return newTestDB(tb, NewSentryDB)
}

func NewTestTx(tb testing.TB) (kv.RwDB, kv.RwTx) {
	tb.Helper()
	return newTestTx(tb, NewTestDB(tb))
}

func NewTestPoolTx(tb testing.TB) (kv.RwDB, kv.RwTx) {
	tb.Helper()
	return newTestTx(tb, NewTestPoolDB(tb))
}

func NewTestSentryTx(tb testing.TB) (kv.RwDB, kv.RwTx) {
	tb.Helper()
	return newTestTx(tb, NewTestSentryDB(tb))
}
