package sharedexec

import (
	"context"
	"crypto/ecdsa"
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
)

func TestParallelOwnerImportMatchesSerialAndUnsignedBuild(t *testing.T) {
	ctx := context.Background()
	var senders []*ecdsa.PrivateKey
	for i := 0; i < 5; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		senders = append(senders, key)
	}
	var genesis *conf.Genesis
	var keys []blscommon.SecretKey
	serial, public, _, template, recipient, _ := unsignedBuildFixtureWithSet(t, 7, func(k []blscommon.SecretKey) { keys = k }, func(g *conf.Genesis) {
		genesis = g
		g.BaseFee = uint256.NewInt(1)
		for _, key := range senders {
			g.Alloc[crypto.PubkeyToAddress(key.PublicKey)] = conf.GenesisAccount{Balance: "0x1000000000000000000"}
		}
	})
	parallel, _ := executionChain(t, genesis, true)
	parallel.SetParallelEVM(true)
	if parallel.GenesisBlock().Hash() != serial.GenesisBlock().Hash() {
		t.Fatal("comparison owners do not share exact genesis")
	}
	cfg := serial.Config()
	leader := hotstuff.New(cfg.HotStuff, cfg)
	defer leader.Close()
	set := public.Engine().ValidatorSetForView(2)
	index := hotstuff.LeaderForView(2, set)
	address, err := set.GetAddress(index)
	if err != nil {
		t.Fatal(err)
	}
	if address != types.HexToAddress("0x03") {
		t.Fatal("fixture no longer exercises RIPEMD160 fee recipient")
	}
	leader.Authorize(address, keys[int(index)])
	if err := leader.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	leader.Engine().RestoreState(2, hotstuff.GenesisQC(), hotstuff.GenesisQC(), 0)
	header := block.CopyHeader(template.Header().(*block.Header))
	if err := leader.Prepare(serial, header); err != nil {
		t.Fatal(err)
	}
	txs := append([]*transaction.Transaction(nil), template.Transactions()...)
	for _, key := range senders {
		tx, err := transaction.SignNewTx(key, transaction.NewLondonSigner(cfg.ChainID), &transaction.LegacyTx{Nonce: 0, To: &recipient, Value: uint256.NewInt(23), Gas: 21000, GasPrice: uint256.NewInt(1)})
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	header.TxHash = block.TxRootAt(txs, header.Time)
	header.ResetHashCache()
	candidateTemplate := block.NewBlock(header, txs).(*block.Block)
	if !internal.NewStateProcessor(cfg, parallel, public).ParallelApplicable(candidateTemplate) {
		t.Fatal("fixture silently falls back to serial execution")
	}
	server, err := New(serial, 8)
	if err != nil {
		t.Fatal(err)
	}
	serialClient := NewClient(startExecutionSocket(t, server), server.Identity())
	defer serialClient.Close()
	built, err := serialClient.Build(ctx, buildRequest(t, server.Identity(), candidateTemplate))
	if err != nil {
		t.Fatal(err)
	}
	var unsigned block.Block
	if err := unsigned.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(serial, &unsigned, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parallelServer, err := New(parallel, 8)
	if err != nil {
		t.Fatal(err)
	}
	parallelClient := NewClient(startExecutionSocket(t, parallelServer), parallelServer.Identity())
	defer parallelClient.Close()
	for i, client := range []*Client{serialClient, parallelClient} {
		result, err := client.Import(ctx, ImportRequest{Identity: client.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
		if err != nil || !result.Durable || result.Result != built.Result || result.Result.GasUsed != 6*21000 {
			t.Fatalf("owner %d differs from actual build: %+v %v", i, result, err)
		}
	}
}
