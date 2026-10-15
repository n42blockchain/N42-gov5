package sharedexec

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

func unsignedBuildFixture(t *testing.T, extra ...func(*conf.Genesis)) (*internal.BlockChain, *hotstuff.HotStuff, *hotstuff.HotStuff, *block.Block, types.Address, *commitment.QMDBRootComputer) {
	return unsignedBuildFixtureWithSet(t, 4, nil, extra...)
}

func unsignedBuildFixtureWithSet(t *testing.T, count int, observeKeys func([]blscommon.SecretKey), extra ...func(*conf.Genesis)) (*internal.BlockChain, *hotstuff.HotStuff, *hotstuff.HotStuff, *block.Block, types.Address, *commitment.QMDBRootComputer) {
	t.Helper()
	// Only the test leader retains its test signing key. The executor receives
	// genesis public keys and initializes a verifier without Authorize.
	var keys []blscommon.SecretKey
	var liveRoot *commitment.QMDBRootComputer
	chain, transfer, recipient := executionFixtureConfigured(t, []bool{true}, func(g *conf.Genesis) {
		for i := 0; i < count; i++ {
			key, err := bls.RandKey()
			if err != nil {
				t.Fatal(err)
			}
			keys = append(keys, key)
			address := types.Address{}
			address[19] = byte(i + 1)
			g.Config.HotStuff.Validators = append(g.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: address.Hex(), BLSKey: "0x" + hex.EncodeToString(key.PublicKey().Marshal())})
		}
		if len(extra) > 0 {
			extra[0](g)
		}
	}, func(root *commitment.QMDBRootComputer) { liveRoot = root })
	if observeKeys != nil {
		observeKeys(keys)
	}
	cfg := chain.Config()
	public := hotstuff.New(cfg.HotStuff, cfg)
	t.Cleanup(func() { _ = public.Close() })
	public.SetRewardFunc(apos.DoReward)
	if err := public.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	chain.SetEngine(public)
	leader := hotstuff.New(cfg.HotStuff, cfg)
	t.Cleanup(func() { _ = leader.Close() })
	vs := public.Engine().ValidatorSetForView(1)
	index := hotstuff.LeaderForView(1, vs)
	address, err := vs.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	leader.Authorize(address, keys[int(index)])
	if err := leader.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	header := transfer.Header().(*block.Header)
	header.Root, header.ReceiptHash, header.Bloom, header.GasUsed = types.Hash{}, types.Hash{}, block.Bloom{}, 0
	if err := leader.Prepare(chain, header); err != nil {
		t.Fatal(err)
	}
	template := block.NewBlock(header, transfer.Transactions()).(*block.Block)
	return chain, public, leader, template, recipient, liveRoot
}

func buildRequest(t *testing.T, identity Identity, template *block.Block) BuildRequest {
	t.Helper()
	raw, err := template.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return BuildRequest{Identity: identity, TemplateHash: crypto.Keccak256Hash(raw), TemplateRLP: raw}
}

func TestUnsignedBuildUnixMatchesActualImportAndKeepsLiveState(t *testing.T) {
	ctx := context.Background()
	chain, public, leader, template, recipient, liveRoot := unsignedBuildFixture(t)
	server, err := New(chain, 4)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	request := buildRequest(t, server.Identity(), template)
	genesisRoot := chain.GenesisBlock().StateRoot()
	var initialNumber uint64
	var initialHash types.Hash
	var initialMarker bool
	if err := chain.DB().View(ctx, func(tx kv.Tx) error {
		var err error
		initialNumber, initialHash, initialMarker, err = rawdb.ReadQMDBApplied(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	assertUnchanged := func() {
		t.Helper()
		if err := chain.DB().View(ctx, func(tx kv.Tx) error {
			n, h, ok, err := rawdb.ReadQMDBApplied(tx)
			if err != nil {
				return err
			}
			if ok != initialMarker || n != initialNumber || h != initialHash {
				t.Fatalf("build moved applied head: %d %s", n, h)
			}
			ibs := state.New(state.NewPlainStateReader(tx))
			if ibs.GetBalance(recipient).Uint64() != 1 {
				t.Fatal("build wrote recipient balance")
			}
			if rawdb.ReadHeadBlockHash(tx) != chain.GenesisBlock().Hash() {
				t.Fatal("build moved canonical")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !chain.HasAppliedBlock(chain.GenesisBlock().Hash(), 0) || liveRoot.Root() != genesisRoot {
			t.Fatal("genesis state changed")
		}
	}
	result, err := client.Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	assertUnchanged()
	var candidate block.Block
	if err := candidate.Unmarshal(result.BlockRLP); err != nil {
		t.Fatal(err)
	}
	header := candidate.Header().(*block.Header)
	if result.Result.Root == (types.Hash{}) || result.Result.GasUsed != 21000 || header.Root != result.Result.Root || header.ReceiptHash != result.Result.ReceiptHash || !bytes.Equal(header.Extra[len(header.Extra)-96:], make([]byte, 96)) {
		t.Fatal("candidate missing unsigned execution result")
	}
	_, key := public.SignerKey()
	if key != nil {
		t.Fatal("executor acquired signing key")
	}
	// A caller may mutate its copy; neither the cache nor another RPC changes.
	owned, err := server.Build(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	owned.BlockRLP[0] ^= 1
	again, err := client.Build(ctx, request)
	if err != nil || !bytes.Equal(again.BlockRLP, result.BlockRLP) {
		t.Fatalf("mutable cache exposed: %v", err)
	}
	if server.BuildCalls() != 1 {
		t.Fatalf("duplicate build executed %d times", server.BuildCalls())
	}
	// The test leader signs externally. The executor never calls Seal.
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	signed := (<-sealed).(*block.Block)
	raw, err := signed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: signed.Hash(), BlockRLP: raw})
	if err != nil || !imported.Durable || imported.Result != result.Result {
		t.Fatalf("actual import differs from build: %+v %v", imported, err)
	}
	if err := chain.DB().View(ctx, func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		if ibs.GetBalance(recipient).Uint64() != 24 {
			t.Fatal("signed import did not execute transfer")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if server.BuildCalls() != 1 || server.ImportCalls() != 1 {
		t.Fatal("build/import counts differ")
	}
	// Build and import currently each execute once. This differential test
	// does NOT claim prepared own-build reuse or seven-validator consensus.
}

func TestUnsignedBuildRejectsTemplatesAndRecoversFromNonceFailure(t *testing.T) {
	ctx := context.Background()
	gapKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	chain, _, _, template, _, liveRoot := unsignedBuildFixture(t, func(g *conf.Genesis) {
		g.Alloc[crypto.PubkeyToAddress(gapKey.PublicKey)] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"}
	})
	server, err := New(chain, 2)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	request := buildRequest(t, server.Identity(), template)
	bad := request
	bad.Identity.GenesisHash[0] ^= 1
	if _, err := client.Build(ctx, bad); err == nil {
		t.Fatal("wrong build identity accepted")
	}
	for _, mutate := range []func(*block.Header){
		func(h *block.Header) { h.Root = types.Hash{1} },
		func(h *block.Header) { h.GasUsed = 1 },
		func(h *block.Header) { h.Extra[len(h.Extra)-1] = 1 },
		func(h *block.Header) { h.Coinbase = types.Address{1} },
		func(h *block.Header) { h.ParentHash = types.Hash{1} },
		func(h *block.Header) { h.Number = uint256.NewInt(2) },
		func(h *block.Header) { h.ReceiptHash = types.Hash{1} },
	} {
		h := template.Header().(*block.Header)
		mutate(h)
		b := block.NewBlock(h, template.Transactions()).(*block.Block)
		if _, err := client.Build(ctx, buildRequest(t, server.Identity(), b)); err == nil {
			t.Fatal("invalid prepared header accepted")
		}
	}
	// Repeating a signed nonce-0 tx rejects one candidate in the lenient
	// builder. Reject the whole template instead of returning a silent subset.
	nonceTxs := []*transaction.Transaction{template.Transactions()[0], template.Transactions()[0]}
	nonceHeader := template.Header().(*block.Header)
	nonceHeader.TxHash = block.TxRootAt(nonceTxs, nonceHeader.Time)
	badNonce := block.NewBlock(nonceHeader, nonceTxs).(*block.Block)
	if _, err := client.Build(ctx, buildRequest(t, server.Identity(), badNonce)); err == nil {
		t.Fatal("rejected nonce candidate published")
	}
	to := types.Address{9}
	gapTx, err := transaction.SignNewTx(gapKey, transaction.NewLondonSigner(chain.Config().ChainID), &transaction.LegacyTx{Nonce: 2, To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	gapHeader := template.Header().(*block.Header)
	gapHeader.TxHash = block.TxRootAt([]*transaction.Transaction{gapTx}, gapHeader.Time)
	if _, err := client.Build(ctx, buildRequest(t, server.Identity(), block.NewBlock(gapHeader, []*transaction.Transaction{gapTx}).(*block.Block))); err == nil {
		t.Fatal("nonce gap published")
	}
	if liveRoot.Root() != chain.GenesisBlock().StateRoot() {
		t.Fatal("failed candidate polluted live tree")
	}
	result, err := client.Build(ctx, request)
	if err != nil || result.Result.GasUsed != 21000 {
		t.Fatalf("failed build poisoned isolated root: %v", err)
	}
	// Distinct prepared view/template hashes must not alias by parent.
	h := template.Header().(*block.Header)
	vs := chain.Config().HotStuff.Validators
	newView := uint64(1) + uint64(len(vs))*hotstuff.LeaderTenure()
	binary.LittleEndian.PutUint64(h.Extra[4:12], newView)
	other := buildRequest(t, server.Identity(), block.NewBlock(h, template.Transactions()).(*block.Block))
	count := server.BuildCalls()
	if _, err := client.Build(ctx, other); err != nil {
		t.Fatal(err)
	}
	if server.BuildCalls() != count+1 {
		t.Fatal("different view reused parent-only candidate cache")
	}
	server.mu.Lock()
	bytes := server.buildBytes
	server.mu.Unlock()
	if bytes <= 0 || bytes > MaxBuildRetainedBytes {
		t.Fatal("invalid retained budget")
	}
	server.Close()
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.buildBytes != 0 || len(server.builds) != 0 {
		t.Fatal("shutdown retained candidate cache")
	}
}

func TestUnsignedBuildCanceledWaiterPreservesActiveBudgetAndOwner(t *testing.T) {
	chain, _, _, template, _, _ := unsignedBuildFixture(t)
	server, err := New(chain, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	request := buildRequest(t, server.Identity(), template)
	server.execute.Lock()
	locked := true
	defer func() {
		if locked {
			server.execute.Unlock()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := server.Build(ctx, request); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		server.mu.Lock()
		active := len(server.builds) == 1
		bytes := server.buildBytes
		server.mu.Unlock()
		if active {
			if bytes != 2*len(request.TemplateRLP)+4096 {
				t.Fatal("active input/output budget not retained")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owner not registered")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("waiter cancel: %v", err)
	}
	h := template.Header().(*block.Header)
	h.Extra[4] += 4
	other := buildRequest(t, server.Identity(), block.NewBlock(h, template.Transactions()).(*block.Block))
	if _, err := server.Build(context.Background(), other); err == nil {
		t.Fatal("active owner evicted at capacity")
	}
	server.execute.Unlock()
	locked = false
	result, err := server.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if server.BuildCalls() != 1 {
		t.Fatal("canceled requester caused duplicate build")
	}
	server.mu.Lock()
	retained := server.buildBytes
	server.mu.Unlock()
	if retained != len(result.BlockRLP) {
		t.Fatal("completed budget does not equal actual retained output")
	}
	if _, _, err := chain.BuildUnsignedCandidate(context.Background(), &block.Block{}); err == nil {
		t.Fatal("zero template accepted")
	}
}
