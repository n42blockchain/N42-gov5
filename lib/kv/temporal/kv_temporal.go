package temporal

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/config3"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/kvcfg"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/order"
	"github.com/n42blockchain/N42/lib/state"
)

// TemporalDB abstracts DB+Snapshots, providing time-travel API for data.
// Uses InvertedIndex (range-scans), History (value at timestamp), and Domain (latest value + history).

type DB struct {
	kv.RwDB
	agg *state.Aggregator
}

func New(db kv.RwDB, agg *state.Aggregator) (*DB, error) {
	if !kvcfg.HistoryV3.FromDB(db) {
		return nil, fmt.Errorf("temporal database requires history.v3 support")
	}
	return &DB{RwDB: db, agg: agg}, nil
}
func (db *DB) Agg() *state.Aggregator { return db.agg }
func (db *DB) InternalDB() kv.RwDB    { return db.RwDB }

// wrapTx wraps a raw kv.Tx into a temporal Tx with aggregator context.
func (db *DB) wrapTx(kvTx kv.Tx) *Tx {
	tx := &Tx{MdbxTx: kvTx.(*mdbx.MdbxTx), db: db}
	tx.aggCtx = db.agg.BeginFilesRo()
	return tx
}

func (db *DB) BeginTemporalRo(ctx context.Context) (kv.TemporalTx, error) {
	kvTx, err := db.RwDB.BeginRo(ctx) //nolint:gocritic
	if err != nil {
		return nil, err
	}
	return db.wrapTx(kvTx), nil
}

func (db *DB) BeginRo(ctx context.Context) (kv.Tx, error) {
	return db.BeginTemporalRo(ctx)
}

func (db *DB) View(ctx context.Context, f func(tx kv.Tx) error) error {
	tx, err := db.BeginTemporalRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return f(tx)
}

func (db *DB) ViewTemporal(ctx context.Context, f func(tx kv.TemporalTx) error) error {
	tx, err := db.BeginTemporalRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return f(tx)
}

func (db *DB) BeginTemporalRw(ctx context.Context) (kv.RwTx, error) {
	kvTx, err := db.RwDB.BeginRw(ctx) //nolint:gocritic
	if err != nil {
		return nil, err
	}
	return db.wrapTx(kvTx), nil
}

func (db *DB) BeginRw(ctx context.Context) (kv.RwTx, error) {
	return db.BeginTemporalRw(ctx)
}

func (db *DB) Update(ctx context.Context, f func(tx kv.RwTx) error) error {
	tx, err := db.BeginTemporalRw(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) BeginTemporalRwNosync(ctx context.Context) (kv.RwTx, error) {
	kvTx, err := db.RwDB.BeginRwNosync(ctx) //nolint:gocritic
	if err != nil {
		return nil, err
	}
	return db.wrapTx(kvTx), nil
}

func (db *DB) BeginRwNosync(ctx context.Context) (kv.RwTx, error) {
	return db.BeginTemporalRwNosync(ctx)
}

func (db *DB) UpdateNosync(ctx context.Context, f func(tx kv.RwTx) error) error {
	tx, err := db.BeginTemporalRwNosync(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

type Tx struct {
	*mdbx.MdbxTx
	db               *DB
	aggCtx           *state.AggregatorRoTx
	resourcesToClose []kv.Closer
}

func (tx *Tx) AggCtx() *state.AggregatorRoTx { return tx.aggCtx }
func (tx *Tx) Agg() *state.Aggregator        { return tx.db.agg }
func (tx *Tx) Rollback() {
	tx.autoClose()
	tx.MdbxTx.Rollback()
}
func (tx *Tx) autoClose() {
	for _, closer := range tx.resourcesToClose {
		closer.Close()
	}
	if tx.aggCtx != nil {
		tx.aggCtx.Close()
	}
}
func (tx *Tx) Commit() error {
	tx.autoClose()
	return tx.MdbxTx.Commit()
}

func (tx *Tx) DomainRange(name kv.Domain, fromKey, toKey []byte, asOfTs uint64, asc order.By, limit int) (it iter.KV, err error) {
	if asc == order.Desc {
		return nil, fmt.Errorf("domain range descending order is not supported")
	}
	switch name {
	case kv.AccountsDomain:
		histStateIt := tx.aggCtx.AccountHistoricalStateRange(asOfTs, fromKey, toKey, limit, tx)
		histStateIt2 := iter.TransformKV(histStateIt, func(k, v []byte) ([]byte, []byte, error) {
			if len(v) == 0 {
				return k[:20], v, nil
			}
			return k[:20], common.Copy(v), nil
		})
		lastestStateIt, err := tx.RangeAscend(kv.PlainState, fromKey, toKey, -1) // don't apply limit, because need filter
		if err != nil {
			return nil, err
		}
		// TODO: instead of iterate over whole storage, need implement iterator which does cursor.Seek(nextAccount)
		latestStateIt2 := iter.FilterKV(lastestStateIt, func(k, v []byte) bool {
			return len(k) == 20
		})
		it = iter.UnionKV(histStateIt2, latestStateIt2, limit)
	case kv.StorageDomain:
		return nil, fmt.Errorf("domain range for %s is not implemented", name)
	case kv.CodeDomain:
		return nil, fmt.Errorf("domain range for %s is not implemented", name)
	default:
		return nil, fmt.Errorf("unexpected domain name: %s", name)
	}

	if closer, ok := it.(kv.Closer); ok {
		tx.resourcesToClose = append(tx.resourcesToClose, closer)
	}

	return it, nil
}
func (tx *Tx) DomainGet(name kv.Domain, key, key2 []byte) (v []byte, ok bool, err error) {
	if config3.EnableHistoryV4InTest {
		return nil, false, fmt.Errorf("DomainGet is unavailable when history v4 test mode is enabled")
	}
	switch name {
	case kv.AccountsDomain:
		v, err = tx.GetOne(kv.PlainState, key)
		return v, v != nil, err
	case kv.StorageDomain:
		v, err = tx.GetOne(kv.PlainState, append(common.Copy(key), key2...))
		return v, v != nil, err
	case kv.CodeDomain:
		v, err = tx.GetOne(kv.Code, key2)
		return v, v != nil, err
	default:
		return nil, false, fmt.Errorf("unexpected domain name: %s", name)
	}
}
func (tx *Tx) DomainGetAsOf(_ kv.Domain, _, _ []byte, _ uint64) (v []byte, ok bool, err error) {
	if config3.EnableHistoryV4InTest {
		return nil, false, fmt.Errorf("DomainGetAsOf is unavailable when history v4 test mode is enabled")
	}
	return nil, false, fmt.Errorf("DomainGetAsOf is not implemented")
}

func (tx *Tx) HistoryGet(name kv.History, key []byte, ts uint64) (v []byte, ok bool, err error) {
	switch name {
	case kv.AccountsHistory:
		v, ok, err = tx.aggCtx.ReadAccountDataNoStateWithRecent(key, ts, tx.MdbxTx)
		if err != nil {
			return nil, false, err
		}
		if !ok || len(v) == 0 {
			return v, ok, nil
		}
		return v, true, nil
	case kv.StorageHistory:
		return tx.aggCtx.ReadAccountStorageNoStateWithRecent2(key, ts, tx.MdbxTx)
	case kv.CodeHistory:
		return tx.aggCtx.ReadAccountCodeNoStateWithRecent(key, ts, tx.MdbxTx)
	default:
		return nil, false, fmt.Errorf("unexpected history name: %s", name)
	}
}

func (tx *Tx) IndexRange(name kv.InvertedIdx, k []byte, fromTs, toTs int, asc order.By, limit int) (timestamps iter.U64, err error) {
	timestamps, err = tx.aggCtx.IndexRange(name, k, fromTs, toTs, asc, limit, tx.MdbxTx)
	if err != nil {
		return nil, err
	}
	if closer, ok := timestamps.(kv.Closer); ok {
		tx.resourcesToClose = append(tx.resourcesToClose, closer)
	}
	return timestamps, nil
}

func (tx *Tx) HistoryRange(name kv.History, fromTs, toTs int, asc order.By, limit int) (it iter.KV, err error) {
	if asc == order.Desc {
		return nil, fmt.Errorf("history range descending order is not supported")
	}
	if limit >= 0 {
		return nil, fmt.Errorf("history range with explicit limit is not implemented")
	}
	switch name {
	case kv.AccountsHistory:
		it, err = tx.aggCtx.AccountHistoryRange(fromTs, toTs, asc, limit, tx)
	case kv.StorageHistory:
		it, err = tx.aggCtx.StorageHistoryRange(fromTs, toTs, asc, limit, tx)
	case kv.CodeHistory:
		it, err = tx.aggCtx.CodeHistoryRange(fromTs, toTs, asc, limit, tx)
	default:
		return nil, fmt.Errorf("unexpected history name: %s", name)
	}
	if err != nil {
		return nil, err
	}
	if closer, ok := it.(kv.Closer); ok {
		tx.resourcesToClose = append(tx.resourcesToClose, closer)
	}
	return it, nil
}
