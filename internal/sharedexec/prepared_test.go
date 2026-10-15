package sharedexec

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

func TestOwnBuildReuseExecutesOnceAndMatchesOrdinaryImport(t *testing.T) {
	for _, contract := range []bool{false, true} {
		t.Run(fmt.Sprint("contract=", contract), func(t *testing.T) { checkOwnBuildReuse(t, contract) })
	}
}

func checkOwnBuildReuse(t *testing.T, contract bool) {
	ctx := context.Background()
	sender, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	var genesis *conf.Genesis
	var keys []blscommon.SecretKey
	chain, _, leader, template, recipient, live := unsignedBuildFixtureWithSet(t, 7, func(k []blscommon.SecretKey) { keys = k }, func(g *conf.Genesis) {
		genesis = g
		if contract {
			g.Alloc[crypto.PubkeyToAddress(sender.PublicKey)] = conf.GenesisAccount{Balance: "0x1000000000000000000"}
			g.Alloc[types.HexToAddress("0x1000")] = conf.GenesisAccount{Balance: "0x1", Code: []byte{0x60, 1, 0x60, 0, 0x55, 0x60, 0, 0x60, 0, 0xa0, 0}, Storage: map[types.Hash]types.Hash{types.Hash{}: types.HexToHash("0x05")}}
		}
	})
	if contract {
		tx, err := transaction.SignNewTx(sender, transaction.NewLondonSigner(genesis.Config.ChainID), &transaction.LegacyTx{Nonce: 0, To: &recipient, Value: uint256.NewInt(23), Gas: 100000, GasPrice: uint256.NewInt(1)})
		if err != nil {
			t.Fatal(err)
		}
		h := block.CopyHeader(template.Header().(*block.Header))
		h.TxHash = block.TxRootAt([]*transaction.Transaction{tx}, h.Time)
		h.ResetHashCache()
		template = block.NewBlock(h, []*transaction.Transaction{tx}).(*block.Block)
	}
	ordinary, _ := executionChain(t, genesis, true)
	public := hotstuff.New(genesis.Config.HotStuff, genesis.Config)
	defer public.Close()
	if err := public.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	ordinary.SetEngine(public)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableOwnBuildReuse(); err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	built, err := client.Build(ctx, buildRequest(t, server.Identity(), template))
	if err != nil {
		t.Fatal(err)
	}
	if chain.PerformanceExecutionAttempts() != 1 {
		t.Fatal("build did not enter real processor exactly once")
	}
	server.execute.Lock()
	prepared := server.prepared
	server.execute.Unlock()
	if prepared == nil || prepared.RetainedBytes() <= 0 || prepared.RetainedBytes() > 128<<20 {
		t.Fatal("candidate write was not retained within budget")
	}
	if live.Root() != chain.GenesisBlock().StateRoot() {
		t.Fatal("prepared build mutated live state")
	}
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	request := ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw}
	result, err := client.Import(ctx, request)
	if err != nil || !result.Durable || result.Result != built.Result {
		t.Fatalf("prepared import: %+v %v", result, err)
	}
	if server.OwnBuildReuses() != 1 || chain.PerformanceExecutionAttempts() != 1 {
		t.Fatal("prepared import re-entered EVM processor")
	}
	if _, err := client.Import(ctx, request); err != nil || server.OwnBuildReuses() != 1 || chain.PerformanceExecutionAttempts() != 1 {
		t.Fatal("durable retry repeated work")
	}
	other, err := New(ordinary, 8)
	if err != nil {
		t.Fatal(err)
	}
	otherClient := NewClient(startExecutionSocket(t, other), other.Identity())
	defer otherClient.Close()
	compared, err := otherClient.Import(ctx, ImportRequest{Identity: other.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !compared.Durable || compared.Result != result.Result {
		t.Fatalf("ordinary import differs: %+v %v", compared, err)
	}
	if ordinary.PerformanceExecutionAttempts() != 1 || other.OwnBuildReuses() != 0 {
		t.Fatal("ordinary path did not execute normally")
	}
	for _, bc := range []interface{ DB() kv.RwDB }{chain, ordinary} {
		if err := bc.DB().View(ctx, func(tx kv.Tx) error {
			if rawdb.ReadHeadBlockHash(tx) != chain.GenesisBlock().Hash() {
				t.Fatal("execution write bypassed CommitQC finality")
			}
			n, hash, ok, err := rawdb.ReadQMDBApplied(tx)
			if err != nil {
				return err
			}
			if !ok || n != 1 || hash != b.Hash() {
				t.Fatal("actual applied marker missing")
			}
			if state.New(state.NewPlainStateReader(tx)).GetBalance(recipient).Uint64() != 24 {
				t.Fatal("prepared plain-state balance differs")
			}
			if contract {
				slot := types.Hash{}
				value, err := state.NewPlainStateReader(tx).ReadAccountStorage(recipient, &slot)
				if err != nil {
					return err
				}
				if new(uint256.Int).SetBytes(value).Uint64() != 1 {
					t.Fatal("prepared storage write differs")
				}
				receipts, err := rawdb.ReadReceiptsByHash(tx, b.Hash())
				if err != nil {
					return err
				}
				if len(receipts) != 1 || receipts[0].Status != 1 || len(receipts[0].Logs) != 1 || receipts[0].Logs[0].BlockHash != b.Hash() {
					t.Fatal("prepared receipt/log was not rebound to sealed hash")
				}
			}

			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	committed, err := client.CommitCertified(ctx, CommitRequest{Identity: server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: 1}, commitProof(keys, b.Hash(), 5, false))
	if err != nil || !committed.Committed || chain.CurrentBlock().Hash() != b.Hash() || chain.PerformanceExecutionAttempts() != 1 {
		t.Fatalf("prepared execution did not finalize through actual 5/7 QC: %+v %v", committed, err)
	}
}

func TestOwnBuildReuseRejectsChangedBodyAndStaleAppliedParent(t *testing.T) {
	ctx := context.Background()
	chain, _, leader, template, recipient, live := unsignedBuildFixtureWithSet(t, 7, nil)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableOwnBuildReuse(); err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	built, err := client.Build(ctx, buildRequest(t, server.Identity(), template))
	if err != nil {
		t.Fatal(err)
	}
	server.execute.Lock()
	prepared := server.prepared
	server.execute.Unlock()
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	var changed block.Block
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := changed.Unmarshal(raw); err != nil {
		t.Fatal(err)
	}
	body := changed.Body().(*block.Body)
	body.Rewards = append(body.Rewards, &block.Reward{Address: recipient, Amount: uint256.NewInt(1)})
	changedRaw, err := changed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: changed.Hash(), BlockRLP: changedRaw})
	if err != nil || result.Durable || result.State != "failed" {
		t.Fatalf("modified body: %+v %v", result, err)
	}
	if live.Root() != chain.GenesisBlock().StateRoot() || chain.PerformanceExecutionAttempts() != 1 {
		t.Fatal("modified body applied state or entered processor")
	}
	// An independent empty sibling wins execution. Keep the original opaque
	// capsule only to exercise the BC's stale-parent guard directly.
	header := block.CopyHeader(template.Header().(*block.Header))
	header.Root = types.Hash{}
	header.ReceiptHash = types.Hash{}
	header.GasUsed = 0
	header.Bloom = block.Bloom{}
	if err := leader.Prepare(chain, header); err != nil {
		t.Fatal(err)
	}
	header.TxHash = block.TxRootAt(nil, header.Time)
	header.ResetHashCache()
	empty := block.NewBlock(header, nil).(*block.Block)
	other, _, err := chain.BuildUnsignedCandidate(ctx, empty)
	if err != nil {
		t.Fatal(err)
	}
	sealed = make(chan block.IBlock, 1)
	if err := leader.Seal(chain, other, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	winner := (<-sealed).(*block.Block)
	winnerRaw, err := winner.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	result, err = client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: winner.Hash(), BlockRLP: winnerRaw})
	if err != nil || !result.Durable {
		t.Fatalf("sibling: %+v %v", result, err)
	}
	root, attempts := live.Root(), chain.PerformanceExecutionAttempts()
	if adopted, err := chain.ImportPreparedCandidate(ctx, b, prepared); adopted || !errors.Is(err, internal.ErrPerformanceParentNotHead) {
		t.Fatalf("stale prepared candidate accepted: %v %v", adopted, err)
	}
	if live.Root() != root || chain.PerformanceExecutionAttempts() != attempts {
		t.Fatal("stale candidate changed root or executed")
	}
	if err := chain.DB().View(ctx, func(tx kv.Tx) error {
		_, hash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok || hash != winner.Hash() {
			t.Fatal("stale candidate moved applied marker")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOwnBuildReuseMissingCacheAndInvalidSealFailSafely(t *testing.T) {
	for _, mode := range []string{"default-off", "dropped", "server-reopened", "invalid-seal", "changed-root"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			chain, _, leader, template, _, live := unsignedBuildFixtureWithSet(t, 7, nil)
			server, err := New(chain, 8)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "default-off" {
				if err := server.EnableOwnBuildReuse(); err != nil {
					t.Fatal(err)
				}
			}
			client := NewClient(startExecutionSocket(t, server), server.Identity())
			defer client.Close()
			built, err := client.Build(ctx, buildRequest(t, server.Identity(), template))
			if err != nil {
				t.Fatal(err)
			}
			if err := server.EnableOwnBuildReuse(); err == nil {
				t.Fatal("backend optimization changed after jobs started")
			}
			var candidate block.Block
			if err := candidate.Unmarshal(built.BlockRLP); err != nil {
				t.Fatal(err)
			}
			sealed := make(chan block.IBlock, 1)
			if err := leader.Seal(chain, &candidate, sealed, make(chan struct{})); err != nil {
				t.Fatal(err)
			}
			b := (<-sealed).(*block.Block)
			if mode == "invalid-seal" || mode == "changed-root" {
				header := block.CopyHeader(b.Header().(*block.Header))
				if mode == "invalid-seal" {
					header.Extra[len(header.Extra)-1] ^= 1
				} else {
					header.Root[0] ^= 1
				}
				header.ResetHashCache()
				bad := block.NewBlockFromStorage(header.Hash(), header, b.Body().(*block.Body))
				if mode == "changed-root" {
					sealed := make(chan block.IBlock, 1)
					if err := leader.Seal(chain, bad, sealed, make(chan struct{})); err != nil {
						t.Fatal(err)
					}
					bad = (<-sealed).(*block.Block)
				}
				raw, err := bad.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				result, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: bad.Hash(), BlockRLP: raw})
				if err != nil || result.Durable || result.State != "failed" {
					t.Fatalf("invalid seal: %+v %v", result, err)
				}
				expected := uint64(1)
				if mode == "changed-root" {
					expected = 2
				}
				if live.Root() != chain.GenesisBlock().StateRoot() || chain.PerformanceExecutionAttempts() != expected {
					t.Fatal("modified header applied state or bypassed validation")
				}
			} else if mode == "dropped" {
				server.execute.Lock()
				server.prepared = nil
				server.execute.Unlock()
			} else if mode == "server-reopened" {
				server.Close()
				replacement, err := New(chain, 8)
				if err != nil {
					t.Fatal(err)
				}
				if err := replacement.EnableOwnBuildReuse(); err != nil {
					t.Fatal(err)
				}
				server = replacement
				client = NewClient(startExecutionSocket(t, server), server.Identity())
				defer client.Close()
			}
			raw, err := b.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
			if err != nil || !result.Durable || result.Result != built.Result {
				t.Fatalf("fallback: %+v %v", result, err)
			}
			expected := uint64(2)
			if mode == "changed-root" {
				expected = 3
			}
			if server.OwnBuildReuses() != 0 || chain.PerformanceExecutionAttempts() != expected {
				t.Fatal("missing/disabled prepared write did not use ordinary execution")
			}
		})
	}
}

type rollbackPreparedWrite struct {
	kv.RwDB
	fail   atomic.Bool
	hash   types.Hash
	panics bool
}

func (db *rollbackPreparedWrite) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	return db.RwDB.Update(ctx, func(tx kv.RwTx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if db.fail.Load() {
			_, hash, ok, err := rawdb.ReadQMDBApplied(tx)
			if err != nil {
				return err
			}
			if ok && hash == db.hash && db.fail.CompareAndSwap(true, false) {
				if db.panics {
					panic("injected prepared write panic")
				}
				return errors.New("injected prepared write rollback")
			}
		}
		return nil
	})
}

func TestOwnBuildReuseWriteRollbackRestoresLiveRootAndFallback(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(fmt.Sprint("panic=", panics), func(t *testing.T) { checkPreparedRollback(t, panics) })
	}
}

func checkPreparedRollback(t *testing.T, panics bool) {
	ctx := context.Background()
	var genesis *conf.Genesis
	_, _, leader, template, recipient, _ := unsignedBuildFixtureWithSet(t, 7, nil, func(g *conf.Genesis) { genesis = g })
	var failing *rollbackPreparedWrite
	chain, live := executionChainUsingDB(t, genesis, func(db kv.RwDB) kv.RwDB { failing = &rollbackPreparedWrite{RwDB: db, panics: panics}; return failing }, true)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.EnableOwnBuildReuse(); err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	built, err := client.Build(ctx, buildRequest(t, server.Identity(), template))
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	failing.hash = b.Hash()
	failing.fail.Store(true)
	request := ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw}
	result, err := client.Import(ctx, request)
	if err != nil || result.Durable || result.State != "failed" {
		t.Fatalf("injected rollback: %+v %v", result, err)
	}
	if live.Root() != chain.GenesisBlock().StateRoot() || chain.PerformanceExecutionAttempts() != 1 || server.OwnBuildReuses() != 0 {
		t.Fatal("failed prepared write left live state ahead or re-executed")
	}
	if err := chain.DB().View(ctx, func(tx kv.Tx) error {
		n, _, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if ok && n != 0 {
			t.Fatal("failed prepared write advanced marker")
		}
		if state.New(state.NewPlainStateReader(tx)).GetBalance(recipient).Uint64() != 1 {
			t.Fatal("failed prepared write persisted balance")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err = client.Import(ctx, request)
	if err != nil || !result.Durable || result.Result != built.Result || chain.PerformanceExecutionAttempts() != 2 {
		t.Fatalf("fallback after rollback: %+v %v", result, err)
	}
}
