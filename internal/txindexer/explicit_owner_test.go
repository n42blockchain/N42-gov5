package txindexer

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/big"
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
	"github.com/n42blockchain/N42/params"
)

func seedExplicitStubCanonical(t *testing.T, db kv.RwDB, chain *stubChain) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := rawdb.WriteCanonicalHash(tx, chain.GenesisBlock().Hash(), 0); err != nil {
			return err
		}
		head := chain.CurrentBlock()
		if err := rawdb.WriteCanonicalHash(tx, head.Hash(), chain.head); err != nil {
			return err
		}
		if err := rawdb.WriteHeaderNumber(tx, head.Hash(), chain.head); err != nil {
			return err
		}
		return rawdb.WriteHotStuffCommittedHead(tx, head.Hash())
	}); err != nil {
		t.Fatal(err)
	}
}

func stubAnchor(t *testing.T, chain *stubChain, end uint64) txlookup.SegmentAnchor {
	t.Helper()
	identity, err := txlookup.CanonicalChainIdentity(chain.Config().ChainID, chain.GenesisBlock().Hash())
	if err != nil {
		t.Fatal(err)
	}
	return txlookup.SegmentAnchor{Identity: identity, EndCanonicalHash: chain.blockAt(end - 1).Hash()}
}

func TestOpenExplicitAtDurableGenesisWithoutCommitMarker(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	chain := &stubChain{head: 0}
	genesis := chain.GenesisBlock()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteCanonicalHash(tx, genesis.Hash(), 0); err != nil {
			return err
		}
		if err := rawdb.WriteHeaderNumber(tx, genesis.Hash(), 0); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, genesis.Hash())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	x, err := OpenExplicit(ctx, chain, db, t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	x.Stop()
}

func TestOrdinaryOwnerRejectsAnchoredSegments(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Setenv("N42_TXINDEX_TAIL", "1")
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	chain := &stubChain{head: 3}
	seedExplicitStubCanonical(t, db, chain)
	if err := db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteCanonicalHash(tx, chain.blockAt(2).Hash(), 2) }); err != nil {
		t.Fatal(err)
	}
	for _, foreign := range []bool{false, true} {
		dir := t.TempDir()
		probe := New(ctx, chain, db, dir)
		anchor := stubAnchor(t, chain, 3)
		if foreign {
			anchor.Identity = types.Hash{0xef}
		}
		if err := txlookup.BuildSegmentFromSourceAnchored(ctx, dir, 1, 3, probe.blockTxSource, anchor, nil); err != nil {
			t.Fatal(err)
		}
		if x, err := OpenFromEnvironment(ctx, chain, db, dir); err == nil || x != nil {
			t.Fatalf("ordinary owner admitted EFD3 foreign=%v: %v, %v", foreign, x, err)
		}
	}
}

func TestOpenExplicitTailMode(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	db := memdb.NewTestDB(t)
	chain := &stubChain{head: 0}
	seedExplicitStubCanonical(t, db, chain)

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
	seedExplicitStubCanonical(t, db, &chain.stubChain)
	if err := db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteCanonicalHash(tx, chain.blockAt(2).Hash(), 2) }); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	probe := New(ctx, chain, db, dir)
	if err := txlookup.BuildSegmentFromSourceAnchored(ctx, dir, 1, 3, probe.blockTxSource, stubAnchor(t, &chain.stubChain, 3), nil); err != nil {
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
	seedExplicitStubCanonical(t, db, chain)
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

func (c *diskChain) Config() *params.ChainConfig { return &params.ChainConfig{ChainID: big.NewInt(1)} }
func (c *diskChain) GenesisBlock() block.IBlock {
	b, _ := c.GetBlockByNumber(uint256.NewInt(0))
	return b
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

// persistedDiskChain takes its chain identity from the opened MDBX database,
// so a copied segment cannot be admitted by changing only a test stub field.
type persistedDiskChain struct{ *diskChain }

func (c *persistedDiskChain) Config() *params.ChainConfig {
	genesis := c.GenesisBlock()
	if genesis == nil {
		return nil
	}
	var cfg *params.ChainConfig
	if err := c.db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		cfg, err = rawdb.ReadChainConfig(tx, genesis.Hash())
		return err
	}); err != nil {
		return nil
	}
	return cfg
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
		genesis := (&stubChain{}).blockAt(0).(*block.Block)
		if err := rawdb.WriteBlock(tx, genesis); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, genesis.Hash(), 0); err != nil {
			return err
		}
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
		if err := tx.Put(modules.ChainConfig, txIndexStartKey, mark[:]); err != nil {
			return err
		}
		last := (&stubChain{}).blockAt(head)
		if err := rawdb.WriteHeaderNumber(tx, last.Hash(), head); err != nil {
			return err
		}
		return rawdb.WriteHotStuffCommittedHead(tx, last.Hash())
	})
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	for _, phase := range []string{"crash", "stale", "reopen"} {
		if phase == "stale" || phase == "reopen" {
			edit := mdbx.NewMDBX(log.New()).Path(dbPath).WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).MustOpen()
			hash := types.Hash{0xef}
			if phase == "reopen" {
				hash = (&stubChain{}).blockAt(6).Hash()
			}
			if err := edit.Update(context.Background(), func(tx kv.RwTx) error { return rawdb.WriteCanonicalHash(tx, hash, 6) }); err != nil {
				edit.Close()
				t.Fatal(err)
			}
			edit.Close()
		}
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
		identity, err := txlookup.CanonicalChainIdentity(chain.Config().ChainID, chain.GenesisBlock().Hash())
		if err != nil {
			t.Fatal(err)
		}
		check := func(seg *txlookup.BuiltSegment) error { return nil }
		if err := txlookup.BuildSegmentFromSourceAnchored(ctx, segmentDir, 2, 7, probe.blockTxSource, txlookup.SegmentAnchor{Identity: identity, EndCanonicalHash: (&stubChain{}).blockAt(6).Hash()}, check); err != nil {
			t.Fatal(err)
		}
		if err := txlookup.BuildSegmentFromSourceAnchored(ctx, segmentDir, 7, 10, probe.blockTxSource, txlookup.SegmentAnchor{Identity: identity, EndCanonicalHash: (&stubChain{}).blockAt(9).Hash()}, check); err != nil {
			t.Fatal(err)
		}
	} else if got := txlookup.SealedEnd(segmentDir); got != 10 {
		t.Fatalf("recovered sealed end = %d, want 10", got)
	}
	x, err := OpenExplicit(ctx, chain, db, segmentDir, true)
	if os.Getenv("TXINDEX_TEST_HELPER_PHASE") == "stale" {
		if err == nil {
			x.Stop()
			t.Fatal("stale same-height canonical segment admitted")
		}
		db.Close()
		return
	}
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

// Two independent MDBX databases can have the same transaction hashes and
// canonical block heights. A copied EFD3 segment must still be rejected by a
// fresh owner process when that database belongs to a different chain ID.
func TestOpenExplicitRejectsForeignDatabaseFreshProcess(t *testing.T) {
	if root := os.Getenv("TXINDEX_FOREIGN_DB_HELPER_ROOT"); root != "" {
		runForeignDatabaseHelper(t, root)
		return
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	root := t.TempDir()
	ctx := context.Background()
	segmentDir := filepath.Join(root, "txindex")
	for _, name := range []string{"owner", "foreign"} {
		chainID := int64(1)
		if name == "foreign" {
			chainID = 2
		}
		db := mdbx.NewMDBX(log.New()).Path(filepath.Join(root, name)).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
			return modules.N42TableCfg
		}).MustOpen()
		err := db.Update(ctx, func(tx kv.RwTx) error {
			for n := uint64(0); n <= 3; n++ {
				blk := (&stubChain{}).blockAt(n).(*block.Block)
				if err := rawdb.WriteBlock(tx, blk); err != nil {
					return err
				}
				if err := rawdb.WriteCanonicalHash(tx, blk.Hash(), n); err != nil {
					return err
				}
			}
			genesis := (&stubChain{}).blockAt(0)
			if err := rawdb.WriteChainConfig(tx, genesis.Hash(), &params.ChainConfig{ChainID: big.NewInt(chainID)}); err != nil {
				return err
			}
			var mark [8]byte
			binary.BigEndian.PutUint64(mark[:], 1)
			if err := tx.Put(modules.ChainConfig, txIndexStartKey, mark[:]); err != nil {
				return err
			}
			last := (&stubChain{}).blockAt(3)
			if err := rawdb.WriteHeaderNumber(tx, last.Hash(), 3); err != nil {
				return err
			}
			return rawdb.WriteHotStuffCommittedHead(tx, last.Hash())
		})
		if err != nil {
			db.Close()
			t.Fatal(err)
		}
		if name == "owner" {
			chain := &persistedDiskChain{&diskChain{db: db, head: 3}}
			identity, err := txlookup.CanonicalChainIdentity(chain.Config().ChainID, chain.GenesisBlock().Hash())
			if err != nil {
				db.Close()
				t.Fatal(err)
			}
			probe := New(ctx, chain, db, segmentDir)
			anchor := txlookup.SegmentAnchor{Identity: identity, EndCanonicalHash: (&stubChain{}).blockAt(2).Hash()}
			if err := txlookup.BuildSegmentFromSourceAnchored(ctx, segmentDir, 1, 3, probe.blockTxSource, anchor, nil); err != nil {
				db.Close()
				t.Fatal(err)
			}
		}
		db.Close()
	}
	for _, phase := range []string{"foreign", "owner"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestOpenExplicitRejectsForeignDatabaseFreshProcess$")
		cmd.Env = append(os.Environ(), "TXINDEX_FOREIGN_DB_HELPER_ROOT="+root, "TXINDEX_FOREIGN_DB_HELPER_PHASE="+phase)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s database helper: %v\n%s", phase, err, out)
		}
		if !strings.Contains(string(out), "FOREIGN_DB_"+strings.ToUpper(phase)) {
			t.Fatalf("%s database helper lacked receipt: %s", phase, out)
		}
	}
}

func runForeignDatabaseHelper(t *testing.T, root string) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()
	phase := os.Getenv("TXINDEX_FOREIGN_DB_HELPER_PHASE")
	if phase != "foreign" && phase != "owner" {
		t.Fatalf("invalid foreign DB helper phase %q", phase)
	}
	db := mdbx.NewMDBX(log.New()).Path(filepath.Join(root, phase)).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return modules.N42TableCfg
	}).MustOpen()
	defer db.Close()
	chain := &persistedDiskChain{&diskChain{db: db, head: 3}}
	identity, err := txlookup.CanonicalChainIdentity(chain.Config().ChainID, chain.GenesisBlock().Hash())
	if err != nil {
		t.Fatal(err)
	}
	segmentDir := filepath.Join(root, "txindex")
	svc, err := txlookup.NewAnchoredService(segmentDir, identity, func(n uint64) (types.Hash, error) {
		var hash types.Hash
		err := db.View(ctx, func(tx kv.Tx) error {
			var err error
			hash, err = rawdb.ReadCanonicalHash(tx, n)
			return err
		})
		return hash, err
	}, 3)
	if phase == "foreign" {
		if err == nil || svc != nil || !strings.Contains(err.Error(), "chain identity mismatch") {
			if svc != nil {
				svc.Close()
			}
			t.Fatalf("foreign chain's segment admission = (%v, %v)", svc, err)
		}
		if x, err := OpenExplicit(ctx, chain, db, segmentDir, true); err == nil || x != nil {
			if x != nil {
				x.Stop()
			}
			t.Fatalf("foreign database owner admitted copied segment: (%v, %v)", x, err)
		}
		fmt.Println("FOREIGN_DB_FOREIGN")
		return
	}
	if err != nil {
		t.Fatalf("original owner's segment rejected: %v", err)
	}
	svc.Close()
	x, err := OpenExplicit(ctx, chain, db, segmentDir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Stop()
	for n := uint64(1); n <= 3; n++ {
		want := txForBlock(n).Hash()
		if err := db.View(ctx, func(tx kv.Tx) error {
			got, err := rawdb.ReadTxLookupEntry(tx, want)
			if err != nil {
				return err
			}
			if got == nil || *got != n {
				return fmt.Errorf("original DB transaction %d lookup = %v", n, got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("FOREIGN_DB_OWNER")
}
