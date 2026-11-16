package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func histAddr(i int) types.Address {
	var a types.Address
	a[0], a[18], a[19] = 0x42, byte(i>>8), byte(i)
	return a
}

// histProofChain writes blocks 0..n through the same steps as the block write
// (ComputeRoot, FlushTo, undo record, history journal, applied marker,
// header), keeping only the last `window` undo records.
type histProofChain struct {
	db     kv.RwDB
	rc     *commitment.QMDBRootComputer
	nonces map[uint64]map[types.Address]uint64
	cur    map[types.Address]uint64
	parent types.Hash
}

func newHistProofChain(t *testing.T, n, window uint64) *histProofChain {
	t.Helper()
	c := &histProofChain{db: newRealignTestDB(t), rc: commitment.NewQMDBRootComputer(),
		nonces: map[uint64]map[types.Address]uint64{}, cur: map[types.Address]uint64{}}
	c.rc.EnableUndoRecording()
	c.rc.EnableLiveHistory()
	for b := uint64(0); b <= n; b++ {
		changes := map[types.Address]*account.StateAccount{}
		for i := 0; i < 40; i++ {
			addr := histAddr(int((b*7 + uint64(i)*3) % 200))
			if b > 0 && i%8 == 7 {
				changes[addr] = nil
				delete(c.cur, addr)
				continue
			}
			a := account.NewAccount()
			a.Nonce = b + 1
			changes[addr] = &a
			c.cur[addr] = a.Nonce
		}
		if err := c.db.Update(context.Background(), func(tx kv.RwTx) error {
			c.rc.SetCold(tx)
			defer c.rc.SetCold(nil)
			root, err := c.rc.ComputeRoot(changes, nil)
			if err != nil {
				return err
			}
			if _, err := c.rc.FlushTo(tx); err != nil {
				return err
			}
			undo := c.rc.LastUndo()
			if err := rawdb.WriteQMDBUndo(tx, b, undo); err != nil {
				return err
			}
			if b == 0 {
				if _, err := c.rc.PrepareLiveHistory(tx, 0, false); err != nil {
					return err
				}
			} else if err := c.rc.RecordBlockHistory(tx, b, undo); err != nil {
				return err
			}
			if b > window {
				if err := rawdb.PruneQMDBUndoBelow(tx, b-window); err != nil {
					return err
				}
			}
			hdr := &block.Header{Number: uint256.NewInt(b), Difficulty: uint256.NewInt(1), Root: root, ParentHash: c.parent}
			rawdb.WriteHeader(tx, hdr)
			if err := rawdb.WriteCanonicalHash(tx, hdr.Hash(), b); err != nil {
				return err
			}
			rawdb.WriteHeadBlockHash(tx, hdr.Hash())
			c.parent = hdr.Hash()
			return rawdb.WriteQMDBApplied(tx, b, hdr.Hash())
		}); err != nil {
			t.Fatalf("block %d: %v", b, err)
		}
		c.rc.CommitFlushed()
		c.rc.EvictFlushed()
		c.rc.TakeUndo()
		snap := map[types.Address]uint64{}
		for a, v := range c.cur {
			snap[a] = v
		}
		c.nonces[b] = snap
	}
	return c
}

func TestQMDBHistoricalProofsBeyondTheWindow(t *testing.T) {
	const n, window = 30, 4
	c := newHistProofChain(t, n, window)

	plain := NewQMDBStateProofProvider(nil)
	hist := NewQMDBStateProofProvider(nil)
	hist.EnableHistory()

	if err := c.db.View(context.Background(), func(tx kv.Tx) error {
		// Without the journal, heights below the window are an explicit error.
		var ue *StateProofUnavailableError
		if _, err := plain.AccountProof(tx, histAddr(1), jsonrpc.BlockNumberOrHashWithNumber(5)); !errors.As(err, &ue) {
			t.Fatalf("plain provider at 5: err = %v, want StateProofUnavailableError", err)
		}
		if ue.Earliest != n-window-1 {
			t.Fatalf("plain earliest = %d, want %d", ue.Earliest, n-window-1)
		}
		r, err := hist.ProvableRange(tx)
		if err != nil || r.Earliest != 0 || r.Latest != n {
			t.Fatalf("history range = %+v, %v; want 0..%d", r, err, n)
		}
		for h := uint64(0); h <= n; h++ {
			hash, err := rawdb.ReadCanonicalHash(tx, h)
			if err != nil {
				return err
			}
			root := rawdb.ReadHeader(tx, hash, h).Root
			for i := 0; i < 200; i += 9 {
				addr := histAddr(i)
				out, err := hist.AccountProof(tx, addr, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(h)))
				if err != nil {
					t.Fatalf("h=%d addr=%d: %v", h, i, err)
				}
				want, live := c.nonces[h][addr]
				if live != (len(out) == 1) {
					t.Fatalf("h=%d addr=%d: live=%v but proof count %d", h, i, live, len(out))
				}
				if !live {
					continue
				}
				blob, err := hexutil.Decode(out[0])
				if err != nil {
					return err
				}
				kh := qmdb.Hash(commitment.AccountKeyHash(addr))
				if !qmdb.VerifyEncodedProofForKey(qmdb.Hash(root), kh, blob) {
					t.Fatalf("h=%d addr=%d: proof does not verify against the header root", h, i)
				}
				pr, err := qmdb.UnmarshalProof(blob)
				if err != nil {
					return err
				}
				var acct account.StateAccount
				if err := commitment.DecodeAccountValue(pr.Value, &acct); err != nil || acct.Nonce != want {
					t.Fatalf("h=%d addr=%d: nonce %d want %d (%v)", h, i, acct.Nonce, want, err)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A journal height whose header root disagrees must be an error, never a
// proof; a broken journal refuses heights from the break on.
func TestQMDBHistoricalProofRootCheckAndBrokenJournal(t *testing.T) {
	const n, window = 12, 3
	c := newHistProofChain(t, n, window)
	hist := NewQMDBStateProofProvider(nil)
	hist.EnableHistory()
	addr := histAddr(7)

	// Tamper with block 3's header root (a distinct header at the canonical slot).
	if err := c.db.Update(context.Background(), func(tx kv.RwTx) error {
		hash, _ := rawdb.ReadCanonicalHash(tx, 3)
		hdr := rawdb.ReadHeader(tx, hash, 3)
		bad := *hdr
		bad.Root = types.Hash{0xde, 0xad}
		rawdb.WriteHeader(tx, &bad)
		if err := rawdb.WriteCanonicalHash(tx, bad.Hash(), 3); err != nil {
			return err
		}
		return commitment.MarkQMDBHistoryBroken(tx, 6)
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.db.View(context.Background(), func(tx kv.Tx) error {
		if out, err := hist.AccountProof(tx, addr, jsonrpc.BlockNumberOrHashWithNumber(3)); err == nil {
			t.Fatalf("tampered header root: got proof %v, want error", out)
		}
		var ue *StateProofUnavailableError
		if _, err := hist.AccountProof(tx, addr, jsonrpc.BlockNumberOrHashWithNumber(7)); !errors.As(err, &ue) {
			t.Fatalf("broken journal at 7: err = %v", err)
		}
		if _, err := hist.AccountProof(tx, addr, jsonrpc.BlockNumberOrHashWithNumber(5)); err != nil {
			t.Fatalf("below the break: %v", err)
		}
		// The undo window still serves heights above the break.
		if _, err := hist.AccountProof(tx, addr, jsonrpc.BlockNumberOrHashWithNumber(n-1)); err != nil {
			t.Fatalf("window height: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = modules.QMDBUndoWindow
}

// Unwinding persisted blocks removes their journal (exact inverse), and an
// unwind the journal cannot follow marks it broken instead of failing.
func TestUnjournalQMDBBlock(t *testing.T) {
	const n, window = 10, 300
	c := newHistProofChain(t, n, window)
	if err := c.db.Update(context.Background(), func(tx kv.RwTx) error {
		for b := uint64(n); b > n-3; b-- {
			undo, err := readQMDBUndo(tx, b)
			if err != nil || undo == nil {
				t.Fatalf("undo %d: %v", b, err)
			}
			if err := unjournalQMDBBlock(tx, true, b, undo); err != nil {
				return err
			}
		}
		_, last, ok, err := commitment.NewMDBXQMDBHistoryStore(tx).BlockPosRange()
		if err != nil || !ok || last != n-3 {
			t.Fatalf("journal ends at %d (ok=%v err=%v), want %d", last, ok, err, n-3)
		}
		if _, broken, err := commitment.ReadQMDBHistoryBroken(tx); err != nil || broken {
			t.Fatalf("exact unwind marked the journal broken (%v)", err)
		}
		// Flag off: untouched.
		if err := unjournalQMDBBlock(tx, false, n-3, nil); err != nil {
			return err
		}
		// No undo record: cannot follow, mark broken.
		if err := unjournalQMDBBlock(tx, true, n-3, nil); err != nil {
			return err
		}
		b, broken, err := commitment.ReadQMDBHistoryBroken(tx)
		if err != nil || !broken || b != n-3 {
			t.Fatalf("broken = %d %v %v, want %d", b, broken, err, n-3)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
