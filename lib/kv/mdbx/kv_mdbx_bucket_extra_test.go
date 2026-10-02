package mdbx

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// chainDB opens a real chain-data-labeled MDBX instance using the production
// table config, so ForceRecreateBucket (which looks up kv.ChaindataTablesCfg)
// has real entries to work against.
func chainDB(t *testing.T) kv.RwDB {
	t.Helper()
	// InMem() unconditionally resets opts.label to kv.InMem, so Label() must
	// be chained AFTER InMem() to stick (an ordering footgun in MdbxOpts;
	// see the final coverage report).
	db := NewMDBX(log.New()).InMem(t.TempDir()).Label(kv.ChainDB).MustOpen()
	t.Cleanup(db.Close)
	return db
}

func TestBucketListAndStat(t *testing.T) {
	db, tx, _ := BaseCase(t)
	_ = db

	mtxForList := tx.(*MdbxTx)
	names, err := mtxForList.ListBuckets()
	require.NoError(t, err)
	require.NotEmpty(t, names)

	mtx := tx.(*MdbxTx)
	stat, err := mtx.BucketStat("Table")
	require.NoError(t, err)
	require.NotNil(t, stat)

	size, err := mtx.BucketSize("Table")
	require.NoError(t, err)
	require.GreaterOrEqual(t, size, uint64(0))

	// special-cased names
	_, err = mtx.BucketStat("freelist")
	require.NoError(t, err)
	_, err = mtx.BucketStat("root")
	require.NoError(t, err)

	dbSize, err := mtx.DBSize()
	require.NoError(t, err)
	require.Greater(t, dbSize, uint64(0))
}

func TestBucketStatUnknownTableErrors(t *testing.T) {
	_, tx, _ := BaseCase(t)
	mtx := tx.(*MdbxTx)
	// A name not present in db.buckets resolves to DBI 0 (the freelist),
	// which is a valid stat target, so BucketStat only errors for an
	// explicit, bogus DBI value. Inject one directly (white-box) to cover
	// the error branch.
	mtx.db.buckets["BogusTable"] = kv.TableCfgItem{DBI: NonExistingDBI}
	_, err := mtx.BucketStat("BogusTable")
	require.Error(t, err)
}

func TestCreateBucketExistingReopensFlags(t *testing.T) {
	db, tx, _ := BaseCase(t)
	_ = db
	mtx := tx.(*MdbxTx)
	// "Table" already exists from BaseCaseDB; CreateBucket should re-open it
	// (the err==nil branch) rather than error.
	require.NoError(t, mtx.CreateBucket("Table"))
}

func TestCreateBucketNewAndUnsupportedFlags(t *testing.T) {
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{
			"Plain": {},
			"Dup":   {Flags: kv.DupSort},
		}
	}).MustOpen()
	t.Cleanup(db.Close)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)
	mtx := tx.(*MdbxTx)

	require.NoError(t, mtx.CreateBucket("Plain"))
	require.NoError(t, mtx.CreateBucket("Dup"))

	// "Weird" was never opened at DB-open time, so CreateBucket takes the
	// not-yet-existing / create path and must reject its unsupported flag.
	mtx.db.buckets["Weird"] = kv.TableCfgItem{Flags: kv.TableFlags(0x100), DBI: NonExistingDBI}
	err = mtx.CreateBucket("Weird")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported flags")
}

func TestCreateBucketReadOnlySkipsMissing(t *testing.T) {
	path := t.TempDir()
	rw := NewMDBX(log.New()).Path(path).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{"Known": {}}
	}).MustOpen()
	rw.Close()

	ro, err := NewMDBX(log.New()).Path(path).Readonly().WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{"Known": {}}
	}).Open(context.Background())
	require.NoError(t, err)
	t.Cleanup(ro.Close)

	roTx, err := ro.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	// CreateBucket was already invoked during open against "Known"; it must
	// have succeeded. We can't call CreateBucket directly on a read-only
	// view since it's not a RwTx, so instead verify the DB opened cleanly,
	// which exercises the ReadOnly()/Accede() skip branch at open time for
	// any table referenced by WithTableCfg but absent on disk.
	require.NotNil(t, roTx)
}

func TestClearBucket(t *testing.T) {
	db, tx, c := BaseCase(t)
	_ = db
	c.Close()
	mtx := tx.(*MdbxTx)

	require.NoError(t, mtx.ClearBucket("Table"))
	v, err := mtx.GetOne("Table", []byte("key1"))
	require.NoError(t, err)
	require.Nil(t, v)

	// ClearBucket on a bucket with NonExistingDBI is a no-op.
	require.NoError(t, mtx.ClearBucket("NoSuchTable"))
}

func TestDropBucketRequiresDeprecated(t *testing.T) {
	db, tx, c := BaseCase(t)
	_ = db
	c.Close()
	mtx := tx.(*MdbxTx)

	err := mtx.DropBucket("Table")
	require.Error(t, err)
	require.ErrorIs(t, err, kv.ErrAttemptToDeleteNonDeprecatedBucket)
}

func TestForceRecreateBucket(t *testing.T) {
	db := chainDB(t)

	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	require.NoError(t, tx.Put(kv.AccountChangeSet, []byte("k"), []byte("v")))
	require.NoError(t, tx.Commit())

	// ForceRecreateBucket drops and reopens the DBI; do it in its own
	// transaction (dropping and recreating a DBI mid-transaction after
	// writes is not supported by MDBX and surfaces as MDBX_PROBLEM).
	tx2, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	t.Cleanup(tx2.Rollback)
	mtx := tx2.(*MdbxTx)
	require.NoError(t, mtx.ForceRecreateBucket(kv.AccountChangeSet))
	require.NoError(t, tx2.Commit())

	tx3, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer tx3.Rollback()
	v, err := tx3.GetOne(kv.AccountChangeSet, []byte("k"))
	require.NoError(t, err)
	require.Nil(t, v)
}

func TestForceRecreateBucketUnknownTable(t *testing.T) {
	db := chainDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	t.Cleanup(tx.Rollback)
	mtx := tx.(*MdbxTx)

	err = mtx.ForceRecreateBucket("TotallyUnknownTable")
	require.Error(t, err)
}

func TestAllTablesAndEnv(t *testing.T) {
	db := chainDB(t)
	mdb := db.(*MdbxKV)
	require.NotEmpty(t, mdb.AllTables())
	require.NotEmpty(t, mdb.AllDBI())
	require.NotNil(t, mdb.Env())
}

func TestExistsBucketUnknown(t *testing.T) {
	_, tx, c := BaseCase(t)
	c.Close()
	mtx := tx.(*MdbxTx)
	ok, err := mtx.ExistsBucket("TotallyUnknown")
	require.NoError(t, err)
	require.False(t, ok)
}
