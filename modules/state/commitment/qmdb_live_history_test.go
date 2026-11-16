package commitment

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

type liveHistChain struct {
	t      *testing.T
	db     kv.RwDB
	rc     *QMDBRootComputer
	hist   bool
	roots  map[uint64]types.Hash
	vals   map[uint64]map[types.Address]uint64 // block -> addr -> nonce (0 = absent)
	cur    map[types.Address]uint64
	undos  map[uint64]*qmdb.BlockUndo
	head   uint64
	nAddrs int
}

func newLiveHistDB(t *testing.T) kv.RwDB {
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	return memdb.NewTestDB(t)
}

func newLiveHistChain(t *testing.T, db kv.RwDB, hist bool) *liveHistChain {
	c := &liveHistChain{t: t, db: db, rc: NewQMDBRootComputer(), hist: hist,
		roots: map[uint64]types.Hash{}, vals: map[uint64]map[types.Address]uint64{},
		cur: map[types.Address]uint64{}, undos: map[uint64]*qmdb.BlockUndo{}, nAddrs: 1500}
	c.rc.EnableUndoRecording()
	if hist {
		c.rc.EnableLiveHistory()
	}
	// Genesis: block 0.
	gen := map[types.Address]*account.StateAccount{}
	for i := 0; i < 400; i++ {
		a := account.NewAccount()
		a.Nonce = 1
		gen[rlAddr(i)] = &a
		c.cur[rlAddr(i)] = 1
	}
	c.write(0, gen, false)
	return c
}

// write executes and persists one block as writeBlockWithState does.
func (c *liveHistChain) write(b uint64, changes map[types.Address]*account.StateAccount, journal bool) {
	t := c.t
	tx, err := c.db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	c.rc.SetCold(tx)
	root, err := c.rc.ComputeRoot(changes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.rc.FlushTo(tx); err != nil {
		t.Fatal(err)
	}
	undo := c.rc.LastUndo()
	if journal {
		if err := c.rc.RecordBlockHistory(tx, b, undo); err != nil {
			t.Fatalf("record %d: %v", b, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	c.rc.CommitFlushed()
	c.rc.EvictFlushed()
	c.rc.TakeUndo()
	c.rc.SetCold(nil)
	c.undos[b] = undo
	c.roots[b] = root
	snap := map[types.Address]uint64{}
	for a, n := range c.cur {
		snap[a] = n
	}
	c.vals[b] = snap
	c.head = b
}

func (c *liveHistChain) block(b, variant uint64) {
	changes := map[types.Address]*account.StateAccount{}
	for i := 0; i < 120; i++ {
		addr := rlAddr(int((b*31 + uint64(i)*11 + variant*5) % uint64(c.nAddrs)))
		if i%10 == 9 {
			changes[addr] = nil // delete
			delete(c.cur, addr)
			continue
		}
		a := account.NewAccount()
		a.Nonce = b*100 + variant + 2
		a.Balance = *uint256.NewInt(b)
		changes[addr] = &a
		c.cur[addr] = a.Nonce
	}
	c.write(b, changes, c.hist)
}

// revertHead unwinds the head block as the sibling-switch path does.
func (c *liveHistChain) revertHead() {
	t := c.t
	b := c.head
	tx, err := c.db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := c.rc.RevertBlock(tx, c.undos[b]); err != nil {
		t.Fatal(err)
	}
	if c.hist {
		if err := RevertQMDBBlockHistory(tx, b, c.undos[b]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	c.rc.CommitFlushed()
	delete(c.roots, b)
	delete(c.vals, b)
	delete(c.undos, b)
	c.head = b - 1
	c.cur = map[types.Address]uint64{}
	for a, n := range c.vals[c.head] {
		c.cur[a] = n
	}
}

// verify proves sampled accounts at every height from a fresh reload of the
// persisted forest (the RPC prover's view).
func (c *liveHistChain) verify(from uint64) {
	t := c.t
	tx, err := c.db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	prover := NewQMDBRootComputer()
	prover.SetCold(tx)
	if err := prover.LoadFrom(tx); err != nil {
		t.Fatal(err)
	}
	store := NewMDBXQMDBHistoryStore(tx)
	for h := from; h <= c.head; h++ {
		for i := 0; i < c.nAddrs; i += 97 {
			addr := rlAddr(i)
			want, live := c.vals[h][addr]
			proof, root, found, err := prover.Tree().ProofAtHeightFrom(store, qmdb.Hash(AccountKeyHash(addr)), h)
			if err != nil {
				t.Fatalf("h=%d addr=%d: %v", h, i, err)
			}
			if root != qmdb.Hash(c.roots[h]) {
				t.Fatalf("h=%d: reconstructed root %x != recorded %x", h, root[:6], c.roots[h].Bytes()[:6])
			}
			if found != live {
				t.Fatalf("h=%d addr=%d found=%v want %v", h, i, found, live)
			}
			if !found {
				continue
			}
			if !qmdb.VerifyEncodedProofForKey(root, qmdb.Hash(AccountKeyHash(addr)), proof.Marshal()) {
				t.Fatalf("h=%d addr=%d: proof does not verify", h, i)
			}
			var acct account.StateAccount
			if err := DecodeAccountValue(proof.Value, &acct); err != nil || acct.Nonce != want {
				t.Fatalf("h=%d addr=%d: nonce %d want %d (%v)", h, i, acct.Nonce, want, err)
			}
		}
	}
}

func (c *liveHistChain) prepare(applied uint64) (uint64, error) { return c.prepareOpt(applied, false) }

func (c *liveHistChain) prepareOpt(applied uint64, reseed bool) (uint64, error) {
	tx, err := c.db.BeginRw(c.t.Context())
	if err != nil {
		c.t.Fatal(err)
	}
	defer tx.Rollback()
	start, err := c.rc.PrepareLiveHistory(tx, applied, reseed)
	if err != nil {
		return 0, err
	}
	return start, tx.Commit()
}

func TestLiveHistoryJournalFromGenesisWithSwitches(t *testing.T) {
	db := newLiveHistDB(t)
	c := newLiveHistChain(t, db, true)
	// Genesis is journaled by the startup check (applied head 0).
	if start, err := c.prepare(0); err != nil || start != 0 {
		t.Fatalf("prepare at genesis: start=%d err=%v", start, err)
	}
	for b := uint64(1); b <= 20; b++ {
		c.block(b, 0)
	}
	c.revertHead()
	c.revertHead()
	for b := uint64(19); b <= 25; b++ {
		c.block(b, 1)
	}
	c.verify(0)

	// Restart: a fresh computer over the same store passes the check.
	c2 := &liveHistChain{t: t, db: db, rc: NewQMDBRootComputer()}
	tx, err := db.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c2.rc.SetCold(tx)
	if err := c2.rc.LoadFrom(tx); err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	c2.rc.SetCold(nil)
	c2.rc.EnableLiveHistory()
	if start, err := c2.prepare(25); err != nil || start != 0 {
		t.Fatalf("prepare after restart: start=%d err=%v", start, err)
	}
	// A wrong applied head is refused (journal above it).
	if _, err := c2.prepare(24); !errors.Is(err, ErrQMDBHistoryUnavailable) {
		t.Fatalf("journal above the applied head: err = %v", err)
	}
}

// Enabling on a database without a journal seeds one at the applied head:
// heights from there on are provable, including across a revert of the seed
// block itself (sibling switch right after enabling).
func TestLiveHistorySeededMidChain(t *testing.T) {
	c := newLiveHistChain(t, newLiveHistDB(t), false)
	for b := uint64(1); b <= 6; b++ {
		c.block(b, 0)
	}
	c.rc.EnableLiveHistory()
	c.hist = true
	if start, err := c.prepare(6); err != nil || start != 6 {
		t.Fatalf("seed: start=%d err=%v", start, err)
	}
	for b := uint64(7); b <= 12; b++ {
		c.block(b, 0)
	}
	c.verify(6)
	// Unwind down to and including the seed block, then take another branch.
	for c.head >= 6 {
		c.revertHead()
	}
	for b := uint64(6); b <= 10; b++ {
		c.block(b, 2)
	}
	// Block 6 was re-executed: re-seeded data must still prove 6..10.
	c.verify(6)
}

func TestLiveHistoryRefusesGapUnlessReseeded(t *testing.T) {
	g := newLiveHistChain(t, newLiveHistDB(t), true)
	if start, err := g.prepare(0); err != nil || start != 0 {
		t.Fatalf("genesis seed: start=%d err=%v", start, err)
	}
	g.block(1, 0)
	g.hist = false
	g.block(2, 0)
	if _, err := g.prepare(2); !errors.Is(err, ErrQMDBHistoryUnavailable) {
		t.Fatalf("gap: err = %v", err)
	}
	if start, err := g.prepareOpt(2, true); err != nil || start != 2 {
		t.Fatalf("reseed: start=%d err=%v", start, err)
	}
	g.hist = true
	g.block(3, 0)
	g.block(4, 1)
	g.verify(2)
}

func TestMarkQMDBHistoryBrokenKeepsEarliest(t *testing.T) {
	db := newLiveHistDB(t)
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		for _, b := range []uint64{9, 5, 7} {
			if err := MarkQMDBHistoryBroken(tx, b); err != nil {
				return err
			}
		}
		got, ok, err := ReadQMDBHistoryBroken(tx)
		if err != nil || !ok || got != 5 {
			t.Fatalf("broken = %d %v %v, want 5", got, ok, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
