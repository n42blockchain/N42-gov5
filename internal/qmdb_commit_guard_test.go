package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

type commitMarkerFaultTx struct {
	kv.RwTx
	err error
}

func (tx commitMarkerFaultTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.DatabaseInfo && string(key) == "qmdbAppliedHead" {
		return nil, tx.err
	}
	return tx.RwTx.GetOne(table, key)
}

type commitMarkerFaultDB struct {
	kv.RwDB
	err error
}

func (db commitMarkerFaultDB) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	return db.RwDB.Update(ctx, func(tx kv.RwTx) error { return fn(commitMarkerFaultTx{RwTx: tx, err: db.err}) })
}

func TestQMDBCanonicalCommitRequiresAppliedLineage(t *testing.T) {
	for _, scenario := range []string{"same-head", "descendant", "sibling", "sibling-descendant", "behind", "missing-marker", "short-marker", "long-marker", "read-failure", "missing-header", "wrong-header"} {
		t.Run(scenario, func(t *testing.T) {
			db := newRealignTestDB(t)
			mk := func(number uint64, parent types.Hash, tag byte) *block.Block {
				return block.NewBlock(&block.Header{Number: uint256.NewInt(number), ParentHash: parent, Difficulty: uint256.NewInt(1), Extra: []byte{tag}}, nil).(*block.Block)
			}
			parent := mk(8, types.Hash{}, 1)
			winner := mk(9, parent.Hash(), 1)
			sibling := mk(9, parent.Hash(), 2)
			child := mk(10, winner.Hash(), 1)
			losingChild := mk(10, sibling.Hash(), 2)
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				for _, b := range []*block.Block{parent, winner, sibling, child, losingChild} {
					if err := rawdb.WriteBlock(tx, b); err != nil {
						return err
					}
				}
				if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 8); err != nil {
					return err
				}
				rawdb.WriteHeadBlockHash(tx, parent.Hash())
				if err := rawdb.WriteHeadHeaderHash(tx, parent.Hash()); err != nil {
					return err
				}
				if err := rawdb.WriteHotStuffCommittedHead(tx, parent.Hash()); err != nil {
					return err
				}
				head := winner
				switch scenario {
				case "descendant", "missing-header", "wrong-header":
					head = child
				case "sibling":
					head = sibling
				case "sibling-descendant":
					head = losingChild
				case "behind":
					head = parent
				case "missing-marker":
					return nil
				case "short-marker":
					return tx.Put(modules.DatabaseInfo, []byte("qmdbAppliedHead"), make([]byte, 39))
				case "long-marker":
					return tx.Put(modules.DatabaseInfo, []byte("qmdbAppliedHead"), make([]byte, 41))
				}
				if err := rawdb.WriteQMDBApplied(tx, head.Number64().Uint64(), head.Hash()); err != nil {
					return err
				}
				if scenario == "missing-header" {
					return tx.Delete(modules.Headers, modules.HeaderKey(10, child.Hash()))
				}
				if scenario == "wrong-header" {
					encoded, err := losingChild.Header().Marshal()
					if err != nil {
						return err
					}
					return tx.Put(modules.Headers, modules.HeaderKey(10, child.Hash()), encoded)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			bc := &BlockChain{ChainDB: db, ctx: context.Background(), qmdbEnabled: true}
			bc.currentBlock.Store(parent)
			calls, hooks := 0, 0
			bc.SetOnBlockCommitted(func(uint64) { calls++ })
			heads := make(chan common.ChainHighestBlock, 4)
			sub, err := event.GlobalEvent.Subscribe(heads)
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Unsubscribe()
			readErr := errors.New("applied marker I/O failure")
			if scenario == "read-failure" {
				bc.ChainDB = commitMarkerFaultDB{RwDB: db, err: readErr}
			}
			err = bc.CommitToCanonicalWith(winner.Hash(), func(kv.RwTx) error { hooks++; return nil })
			valid := scenario == "same-head" || scenario == "descendant"
			if scenario != "read-failure" && bc.HasAppliedBlock(winner.Hash(), 9) != valid {
				t.Fatal("vote probe and canonical commit disagree about execution evidence")
			}
			if (err == nil) != valid {
				t.Fatalf("canonical commit: err=%v valid=%v", err, valid)
			}
			if scenario == "read-failure" && !errors.Is(err, readErr) {
				t.Fatalf("lost read error: %v", err)
			}
			want := parent.Hash()
			wantCalls := 0
			if valid {
				want = winner.Hash()
				wantCalls = 1
			}
			if bc.CurrentBlock().Hash() != want || calls != wantCalls || hooks != wantCalls || len(heads) != wantCalls {
				t.Fatalf("head/hook/event changed incorrectly: head=%s calls=%d hooks=%d events=%d", bc.CurrentBlock().Hash(), calls, hooks, len(heads))
			}
			if err := db.View(context.Background(), func(tx kv.Tx) error {
				if rawdb.ReadHeadBlockHash(tx) != want || rawdb.ReadHeadHeaderHash(tx) != want || rawdb.ReadHotStuffCommittedHead(tx) != want {
					t.Fatal("disk head changed on rejected commit")
				}
				canonical, err := rawdb.ReadCanonicalHash(tx, 9)
				if err != nil {
					return err
				}
				if valid && canonical != winner.Hash() || !valid && canonical != (types.Hash{}) {
					t.Fatal("canonical rows changed incorrectly")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if !valid {
				// A rejected commit remains retryable once execution has aligned
				// and atomically published the winning applied marker.
				bc.ChainDB = db
				if err := db.Update(context.Background(), func(tx kv.RwTx) error {
					return rawdb.WriteQMDBApplied(tx, 9, winner.Hash())
				}); err != nil {
					t.Fatal(err)
				}
				if err := bc.CommitToCanonicalWith(winner.Hash(), func(kv.RwTx) error { hooks++; return nil }); err != nil {
					t.Fatalf("aligned retry failed: %v", err)
				}
				if bc.CurrentBlock().Hash() != winner.Hash() || calls != 1 || hooks != 1 || len(heads) != 1 {
					t.Fatal("aligned retry did not publish exactly one committed head")
				}
			}
		})
	}
}

func TestQMDBAppliedProbeRejectsMissingOrMalformedMarker(t *testing.T) {
	db := newRealignTestDB(t)
	header := &block.Header{Number: uint256.NewInt(5), Difficulty: uint256.NewInt(1)}
	bc := &BlockChain{ChainDB: db, ctx: context.Background(), qmdbEnabled: true}
	for _, value := range [][]byte{nil, {1}, make([]byte, 39), make([]byte, 41)} {
		if err := db.Update(context.Background(), func(tx kv.RwTx) error {
			rawdb.WriteHeader(tx, header)
			if value == nil {
				return tx.Delete(modules.DatabaseInfo, []byte("qmdbAppliedHead"))
			}
			return tx.Put(modules.DatabaseInfo, []byte("qmdbAppliedHead"), value)
		}); err != nil {
			t.Fatal(err)
		}
		if bc.HasAppliedBlock(header.Hash(), 5) {
			t.Fatalf("stored header counted as executed with %d-byte marker", len(value))
		}
	}
}

func TestQMDBGenesisRecordsVerifiedAppliedState(t *testing.T) {
	for _, override := range []types.Hash{{}, {1}} {
		t.Run(override.Hex(), func(t *testing.T) {
			db := newRealignTestDB(t)
			genesis := &GenesisBlock{GenesisConfig: &conf.Genesis{
				Config: &params.ChainConfig{StateScheme: qmdbStateScheme}, Alloc: conf.GenesisAlloc{}, StateRoot: override,
			}}
			if err := db.Update(context.Background(), func(tx kv.RwTx) error {
				b, _, err := genesis.WriteGenesisState(tx)
				if err != nil {
					return err
				}
				number, hash, ok, err := rawdb.ReadQMDBApplied(tx)
				if override != (types.Hash{}) {
					if ok || err != nil {
						t.Fatalf("unverified override produced applied evidence: ok=%v err=%v", ok, err)
					}
					return nil
				}
				if err != nil || !ok || number != 0 || hash != b.Hash() {
					t.Fatalf("genesis marker: n=%d hash=%x ok=%v err=%v", number, hash, ok, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
