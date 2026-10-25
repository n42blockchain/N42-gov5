package txindexer

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestOpenExplicitTailMode(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	chain := &stubChain{head: 0}

	if x, err := OpenExplicit(ctx, chain, db, t.TempDir(), false); err != nil || x != nil {
		t.Fatalf("off without watermark = (%v, %v), want (nil, nil)", x, err)
	}
	t.Setenv("N42_TXINDEX_TAIL", "0")
	x, err := OpenExplicit(ctx, chain, db, t.TempDir(), true)
	if err != nil || x == nil || !x.Enabled() {
		t.Fatalf("typed on must override legacy environment: (%v, %v)", x, err)
	}
	x.Stop()

	if x, err := OpenExplicit(ctx, chain, db, t.TempDir(), false); err == nil || x != nil || !strings.Contains(err.Error(), "previously enabled") {
		t.Fatalf("off after watermark must fail closed: (%v, %v)", x, err)
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.ChainConfig, txIndexStartKey, []byte{1})
	}); err != nil {
		t.Fatal(err)
	}
	if x, err := OpenExplicit(ctx, chain, db, t.TempDir(), false); err == nil || x != nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("malformed watermark must fail closed: (%v, %v)", x, err)
	}
}

func TestOpenFromEnvironmentFailsClosedAfterTailWatermark(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	chain := &stubChain{head: 0}
	t.Setenv("N42_TXINDEX_TAIL", "0")
	if x, err := OpenFromEnvironment(ctx, chain, db, t.TempDir()); err != nil || x != nil {
		t.Fatalf("ordinary off before watermark = (%v, %v)", x, err)
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.ChainConfig, txIndexStartKey, []byte{0, 0, 0, 0, 0, 0, 0, 1})
	}); err != nil {
		t.Fatal(err)
	}
	if x, err := OpenFromEnvironment(ctx, chain, db, t.TempDir()); err == nil || x != nil || !strings.Contains(err.Error(), "previously enabled") {
		t.Fatalf("ordinary off after watermark must fail closed: (%v, %v)", x, err)
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.ChainConfig, txIndexStartKey, []byte{1})
	}); err != nil {
		t.Fatal(err)
	}
	if x, err := OpenFromEnvironment(ctx, chain, db, t.TempDir()); err == nil || x != nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("ordinary malformed watermark must fail closed: (%v, %v)", x, err)
	}
}

func TestLegacyBuildFailureRetriesButExplicitOwnerStops(t *testing.T) {
	t.Setenv("N42_TXINDEX_SEAL_MIN_TX", "1")
	t.Setenv("N42_TXINDEX_KEEP_BLOCKS", "1")
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, explicit := range []bool{false, true} {
		x := New(context.Background(), nil, nil, blocked)
		x.txTail = txlookup.NewTail()
		for n := uint64(1); n <= 3; n++ {
			x.txTail.Add(n, []types.Hash{txForBlock(n).Hash()})
		}
		if explicit {
			x.explicitEnabled = &explicit
		}
		for attempt := 0; attempt < 2; attempt++ {
			if x.sealTxIndexOnce() {
				t.Fatal("unpublished segment unexpectedly sealed")
			}
			if x.txTail.Len() != 3 {
				t.Fatal("failed build dropped answerable transactions")
			}
		}
		if (x.FatalError() != nil) != explicit {
			t.Fatalf("explicit=%v fatal=%v", explicit, x.FatalError())
		}
		select {
		case <-x.FatalSeal():
			if !explicit {
				t.Fatal("legacy prepublication failure reported fatal")
			}
			if x.FatalError() == nil {
				t.Fatal("fatal error disappeared after notification was received")
			}
		default:
			if explicit {
				t.Fatal("explicit owner did not report fatal build failure")
			}
		}
	}
}

type failingCanonicalChain struct {
	stubChain
	fail atomic.Bool
}

func (c *failingCanonicalChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	if c.fail.Load() && number.Uint64() == 1 {
		return nil, fmt.Errorf("canonical body unavailable")
	}
	return c.stubChain.GetBlockByNumber(number)
}

func TestExplicitTailPropagatesCanonicalBodyFailure(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		var mark [8]byte
		binary.BigEndian.PutUint64(mark[:], 1)
		return tx.Put(modules.ChainConfig, txIndexStartKey, mark[:])
	}); err != nil {
		t.Fatal(err)
	}
	chain := &failingCanonicalChain{stubChain: stubChain{head: 3}}
	dir := t.TempDir()
	probe := New(ctx, chain, db, dir)
	if err := txlookup.BuildSegmentFromSource(ctx, dir, 1, 3, probe.blockTxSource); err != nil {
		t.Fatal(err)
	}
	x, err := OpenExplicit(ctx, chain, db, dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()
	chain.fail.Store(true)
	if err := db.View(ctx, func(tx kv.Tx) error {
		_, err := rawdb.ReadTxLookupEntry(tx, txForBlock(1).Hash())
		return err
	}); err == nil || !strings.Contains(err.Error(), "canonical body unavailable") {
		t.Fatalf("sealed tx canonical read failure hidden as miss: %v", err)
	}
}

func TestOpenExplicitTailRejectsPartialPublishedIndex(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		var mark [8]byte
		binary.BigEndian.PutUint64(mark[:], 1)
		return tx.Put(modules.ChainConfig, txIndexStartKey, mark[:])
	}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	chain := &stubChain{head: 3}
	probe := New(ctx, chain, db, dir)
	if err := txlookup.BuildSegmentFromSource(ctx, dir, 1, 3, probe.blockTxSource); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "txindex.cidx"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if x, err := OpenExplicit(ctx, chain, db, dir, true); err == nil || x != nil {
		t.Fatalf("partial cidx must reject typed startup: (%v, %v)", x, err)
	}
}

// diskChain reads actual canonical block data on every lookup. It deliberately
// owns no transactions that can survive a helper-process exit.
type diskChain struct {
	db   kv.RwDB
	head uint64
}

func (c *diskChain) CurrentBlock() block.IBlock {
	b, _ := c.GetBlockByNumber(uint256.NewInt(c.head))
	return b
}

func (c *diskChain) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	var b *block.Block
	err := c.db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		b, err = rawdb.ReadBlockByNumber(tx, number.Uint64())
		return err
	})
	return b, err
}

// The first helper exits without Stop or db.Close, modeling a process crash.
// The second helper must reconstruct every lookup from the physical MDBX row,
// sealed segment, or durable canonical block after a fresh process start.
func TestOpenExplicitTailFreshProcessLookup(t *testing.T) {
	if path := os.Getenv("TXINDEX_TEST_HELPER_PATH"); path != "" {
		runExplicitTailHelper(t, path)
		return
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	root := t.TempDir()
	dbPath := filepath.Join(root, "chaindata")
	db := mdbx.NewMDBX(log.New()).Path(dbPath).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return modules.N42TableCfg
	}).MustOpen()
	const head = 12
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for n := uint64(1); n <= head; n++ {
			b := (&stubChain{}).blockAt(n).(*block.Block)
			if err := rawdb.WriteBlock(tx, b); err != nil {
				return err
			}
			if err := rawdb.WriteCanonicalHash(tx, b.Hash(), n); err != nil {
				return err
			}
			if n == 1 {
				rawdb.WriteTxLookupEntries(tx, b)
			}
		}
		var mark [8]byte
		binary.BigEndian.PutUint64(mark[:], 2)
		return tx.Put(modules.ChainConfig, txIndexStartKey, mark[:])
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	for _, phase := range []string{"crash", "reopen"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOpenExplicitTailFreshProcessLookup$")
		cmd.Env = append(os.Environ(), "TXINDEX_TEST_HELPER_PATH="+root, "TXINDEX_TEST_HELPER_PHASE="+phase)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s helper: %v\n%s", phase, err, out)
		}
	}
}

func runExplicitTailHelper(t *testing.T, root string) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	db := mdbx.NewMDBX(log.New()).Path(filepath.Join(root, "chaindata")).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return modules.N42TableCfg
	}).MustOpen()
	const head = 12
	chain := &diskChain{db: db, head: head}
	segmentDir := filepath.Join(root, "txindex")
	if os.Getenv("TXINDEX_TEST_HELPER_PHASE") == "crash" {
		probe := New(ctx, chain, db, segmentDir)
		if err := txlookup.BuildSegmentFromSource(ctx, segmentDir, 2, 7, probe.blockTxSource); err != nil {
			t.Fatal(err)
		}
		if err := txlookup.BuildSegmentFromSource(ctx, segmentDir, 7, 10, probe.blockTxSource); err != nil {
			t.Fatal(err)
		}
	} else if got := txlookup.SealedEnd(segmentDir); got != 10 {
		t.Fatalf("recovered sealed end = %d, want 10", got)
	}
	x, err := OpenExplicit(ctx, chain, db, segmentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	for n := uint64(1); n <= head; n++ {
		want := txForBlock(n).Hash()
		if err := db.View(ctx, func(tx kv.Tx) error {
			got, err := rawdb.ReadTxLookupEntry(tx, want)
			if err != nil {
				return err
			}
			if got == nil || *got != n {
				return fmt.Errorf("hash of block %d resolved to %v", n, got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("TXINDEX_TEST_HELPER_PHASE") == "crash" {
		// Model process crash or metadata loss after cidx is durable but
		// before the advisory variable-width sidecar reaches disk. The next
		// process must use the
		// committed frames themselves rather than invent SegmentSize widths.
		if err := os.Remove(filepath.Join(segmentDir, "txindex.ranges")); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	x.Stop()
	db.Close()
}
