package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// Test-only header engine. Real BlockChain, StateProcessor, EVM, signatures,
// QMDB and receipts execute; this is not a seven-validator consensus test.
type executionTestEngine struct{ consensus.Engine }

type commitOnlyTestEngine struct{ executionTestEngine }

func (commitOnlyTestEngine) Type() params.ConsensusType { return params.HotStuffConsensus }

func (executionTestEngine) Finalize(_ consensus.ChainHeaderReader, ih block.IHeader, ibs *state.IntraBlockState, _ []*transaction.Transaction, _ []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	root := ibs.IntermediateRoot()
	header := ih.(*block.Header)
	if header.Root != (types.Hash{}) && header.Root != root {
		return nil, nil, fmt.Errorf("test execution root mismatch: header %s local %s", header.Root, root)
	}
	return nil, nil, nil
}

func executionChain(t *testing.T, genesis *conf.Genesis, commitOnly ...bool) (*internal.BlockChain, *commitment.QMDBRootComputer) {
	t.Helper()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	var gb *block.Block
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		var err error
		gb, _, err = (&internal.GenesisBlock{GenesisConfig: genesis}).Write(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var engine consensus.Engine = executionTestEngine{apos.NewFaker()}
	if len(commitOnly) > 0 && commitOnly[0] {
		engine = commitOnlyTestEngine{executionTestEngine{apos.NewFaker()}}
	}
	chain, err := internal.NewBlockChain(context.Background(), gb, engine, db, nil, genesis.Config)
	if err != nil {
		t.Fatal(err)
	}
	bc := chain.(*internal.BlockChain)
	t.Cleanup(func() { _ = bc.Close() })
	root := commitment.NewQMDBRootComputer()
	if err := db.View(context.Background(), func(tx kv.Tx) error { root.SetCold(tx); return root.LoadFrom(tx) }); err != nil {
		t.Fatal(err)
	}
	root.SetCold(nil)
	root.EnableUndoRecording()
	bc.SetQMDBRootComputer(root)
	return bc, root
}

func executionFixture(t *testing.T, commitOnly ...bool) (*internal.BlockChain, *block.Block, types.Address) {
	t.Helper()
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1000")
	genesis.Alloc = conf.GenesisAlloc{from: {Balance: "0x56bc75e2d63100000"}, to: {Balance: "0x1"}}
	owner, _ := executionChain(t, &genesis, commitOnly...)
	builder, root := executionChain(t, &genesis)
	txn, err := transaction.SignNewTx(key, transaction.NewLondonSigner(genesis.Config.ChainID), &transaction.LegacyTx{
		Nonce: 0, To: &to, Value: uint256.NewInt(23), Gas: 21000, GasPrice: uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	header := &block.Header{ParentHash: builder.GenesisBlock().Hash(), Number: uint256.NewInt(1), GasLimit: genesis.GasLimit,
		Time: genesis.Timestamp + 1, Difficulty: uint256.NewInt(1), BaseFee: uint256.NewInt(1), GasUsed: 21000}
	candidate := block.NewBlock(header, []*transaction.Transaction{txn}).(*block.Block)
	var receipts block.Receipts
	if err := builder.DB().View(context.Background(), func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		ibs := state.New(reader)
		ibs.SetDetachedPendingCredits(true)
		ibs.SetRootComputer(root)
		processor := internal.NewStateProcessor(genesis.Config, builder, executionTestEngine{apos.NewFaker()})
		var err error
		receipts, _, _, header.GasUsed, err = processor.Process(candidate, ibs, reader, state.NewNoopWriter(), nil)
		if err != nil {
			return err
		}
		header.Root, _ = ibs.LastIntermediateRoot()
		result := internal.ExecutedResultFor(genesis.Config, 1, header.Root, receipts)
		header.ReceiptHash, header.Bloom = result.ReceiptHash, result.Bloom
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	built := block.NewBlockFromReceipt(header, []*transaction.Transaction{txn}, nil, receipts, nil).(*block.Block)
	return owner, built, to
}

func startExecutionSocket(t *testing.T, server *Server) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "execute.sock")
	httpServer, listener, err := server.ListenUnix(socket)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close(); server.Close() })
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	return socket
}

func TestSevenClientsShareOneRealExecution(t *testing.T) {
	chain, b, to := executionFixture(t)
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	socket := startExecutionSocket(t, server)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	request := ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw}
	var executions atomic.Uint64
	chain.SetExecutedHook(func(types.Hash, types.Hash, types.Hash, uint64, []byte) { executions.Add(1) })
	results := make(chan ImportResult, 7)
	errors := make(chan error, 7)
	var wg sync.WaitGroup
	barrier := make(chan struct{})
	for range 7 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := NewClient(socket, server.Identity())
			defer client.Close()
			<-barrier
			result, err := client.Import(context.Background(), request)
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}()
	}
	close(barrier)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	close(results)
	count := 0
	for result := range results {
		count++
		if !result.Checked || !result.Executed || !result.Durable || result.State != "durable" || result.Result.Root != b.StateRoot() || result.Result.GasUsed != 21000 {
			t.Fatalf("non-executed response: %+v", result)
		}
	}
	if count != 7 || server.ImportCalls() != 1 {
		t.Fatalf("clients=%d real import calls=%d", count, server.ImportCalls())
	}
	deadline := time.Now().Add(time.Second)
	for executions.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if executions.Load() != 1 {
		t.Fatalf("actual execution hook count=%d", executions.Load())
	}
	if err := chain.DB().View(context.Background(), func(tx kv.Tx) error {
		account, err := state.NewPlainStateReader(tx).ReadAccountData(to)
		if err != nil {
			return err
		}
		if account == nil || account.Balance.Uint64() != 24 {
			t.Fatalf("transfer state did not execute: %+v", account)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionRejectsIdentityMalformedAndUnknownFields(t *testing.T) {
	chain, b, _ := executionFixture(t)
	server, err := New(chain, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, _ := b.Marshal()
	request := ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw}
	wrong := request
	wrong.Identity.GenesisHash[31] ^= 1
	if _, err := server.Import(context.Background(), wrong); err == nil {
		t.Fatal("wrong chain accepted")
	}
	wrong = request
	wrong.BlockHash[31] ^= 1
	if _, err := server.Import(context.Background(), wrong); err == nil {
		t.Fatal("wrong block hash accepted")
	}
	wrong = request
	wrong.BlockRLP = []byte{0xff}
	if _, err := server.Import(context.Background(), wrong); err == nil {
		t.Fatal("malformed RLP accepted")
	}
	header := b.Header().(*block.Header)
	header.TxHash[31] ^= 1
	wrongBody := b.WithSeal(header)
	wrong.BlockHash = wrongBody.Hash()
	wrong.BlockRLP, _ = wrongBody.Marshal()
	if _, err := server.Import(context.Background(), wrong); err == nil {
		t.Fatal("header/body root mismatch accepted")
	}
	frame := block.NewFrameBlockFromReceipt(b.Header(), []types.Hash{{1}}, nil, nil, nil).(*block.Block)
	wrong.BlockHash = frame.Hash()
	wrong.BlockRLP, _ = frame.Marshal()
	if _, err := server.Import(context.Background(), wrong); err == nil {
		t.Fatal("unimplemented frame transport accepted")
	}
	encoded, _ := json.Marshal(request)
	encoded = append(encoded[:len(encoded)-1], []byte(`,"sender_claims":["trusted"]}`)...)
	r := httptest.NewRequest(http.MethodPost, "/v1/import", bytes.NewReader(encoded))
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest || server.ImportCalls() != 0 {
		t.Fatal("client sender claims bypassed validation")
	}
}

func TestExecutionDeferredConfigurationFailsClosed(t *testing.T) {
	chain, _, _ := executionFixture(t)
	chain.Config().DeferredExecutionTime = big.NewInt(1)
	if _, err := New(chain, 1); err == nil {
		t.Fatal("deferred held execution enabled before result transport")
	}
}

func TestExecutionTransientAndCanceledWaiterNeverCachesOrDuplicates(t *testing.T) {
	chain, b, _ := executionFixture(t)
	server, err := New(chain, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	header := b.Header().(*block.Header)
	header.ParentHash[31] ^= 1
	unknown := block.NewBlock(header, b.Transactions()).(*block.Block)
	raw, _ := unknown.Marshal()
	request := ImportRequest{Identity: server.Identity(), BlockHash: unknown.Hash(), BlockRLP: raw}
	for range 2 {
		result, err := server.Import(context.Background(), request)
		if err != nil || result.State != "accepted" || result.Executed || result.Durable {
			t.Fatalf("parent wait reported execution: %+v %v", result, err)
		}
		server.mu.Lock()
		count := len(server.jobs)
		server.mu.Unlock()
		if count != 0 {
			t.Fatal("transient result cached")
		}
	}
	if server.ImportCalls() != 0 {
		t.Fatal("unknown parent executed")
	}
	originalRaw, _ := b.Marshal()
	valid := ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: originalRaw}
	// Hold the owner before import, cancel its requester, and attach a waiter.
	server.execute.Lock()
	var unlock sync.Once
	defer unlock.Do(server.execute.Unlock)
	ctx, cancel := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() { _, err := server.Import(ctx, valid); ownerDone <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		count := len(server.jobs)
		server.mu.Unlock()
		if count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner did not register")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-ownerDone; err != context.Canceled {
		t.Fatalf("cancel result=%v", err)
	}
	server.mu.Lock()
	count := len(server.jobs)
	server.mu.Unlock()
	if count != 1 {
		t.Fatal("request cancellation deleted running ownership")
	}
	// A different hash cannot evict this active owner even after cancellation.
	if _, err := server.Import(context.Background(), request); err == nil {
		t.Fatal("capacity evicted a running job")
	}
	unlock.Do(server.execute.Unlock)
	result, err := server.Import(context.Background(), valid)
	if err != nil || !result.Durable || server.ImportCalls() != 1 {
		t.Fatalf("canceled requester caused lost or duplicate execution: %+v %v calls=%d", result, err, server.ImportCalls())
	}
}
