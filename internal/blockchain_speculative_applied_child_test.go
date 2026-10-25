package internal

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestCopySpeculativeSenderMemosRequiresExactSignedFieldsAndForkSigner(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	config := &params.ChainConfig{ChainID: big.NewInt(97), LondonBlock: big.NewInt(0)}
	header := &block.Header{Number: uint256.NewInt(2), Time: 3}
	signer := transaction.MakeSignerWithTimestamp(config, header.Number.ToBig(), header.Time)
	to := types.Address{0x42}
	signed, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{To: &to, Value: uint256.NewInt(1), Gas: 21000, GasPrice: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	from, err := signer.Sender(signed)
	if err != nil {
		t.Fatal(err)
	}
	signed.SetSenderMemo(signer, from)
	wire, err := transaction.EncodeEthereumTransaction(signed)
	if err != nil {
		t.Fatal(err)
	}
	copyTx, err := transaction.DecodeEthereumTransaction(wire)
	if err != nil {
		t.Fatal(err)
	}
	copySpeculativeSenderMemos(config, header, []*transaction.Transaction{signed}, []*transaction.Transaction{copyTx})
	if !copyTx.HasSenderMemo(signer) {
		t.Fatal("exact signed copy lost verified sender memo")
	}
	other, err := transaction.SignNewTx(key, signer, &transaction.LegacyTx{To: &to, Value: uint256.NewInt(2), Gas: 21000, GasPrice: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	copySpeculativeSenderMemos(config, header, []*transaction.Transaction{signed}, []*transaction.Transaction{other})
	if other.HasSenderMemo(signer) {
		t.Fatal("different signed fields received sender memo")
	}
	otherConfig := &params.ChainConfig{ChainID: big.NewInt(97)}
	otherSigner := transaction.MakeSignerWithTimestamp(otherConfig, header.Number.ToBig(), header.Time)
	preFork, err := transaction.DecodeEthereumTransaction(wire)
	if err != nil {
		t.Fatal(err)
	}
	copySpeculativeSenderMemos(otherConfig, header, []*transaction.Transaction{signed}, []*transaction.Transaction{preFork})
	if preFork.HasSenderMemo(otherSigner) {
		t.Fatal("different fork signer received sender memo")
	}
}

func TestReadSpeculativeAppliedParentAccountsPinsMarkerAndPlainState(t *testing.T) {
	db := newRealignTestDB(t)
	ctx := context.Background()
	genesis := block.NewBlock(&block.Header{Number: uint256.NewInt(0), Difficulty: uint256.NewInt(0)}, nil).(*block.Block)
	address := types.Address{7}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, genesis); err != nil {
			return err
		}
		if err := rawdb.WriteQMDBApplied(tx, 0, genesis.Hash()); err != nil {
			return err
		}
		writer := state.NewPlainStateWriter(tx, tx, 0)
		before := account.NewAccount()
		after := account.NewAccount()
		after.Nonce = 3
		after.Balance.SetUint64(11)
		return writer.UpdateAccountData(address, &before, &after)
	}); err != nil {
		t.Fatal(err)
	}
	bc := &BlockChain{ChainDB: db, engine: &hotstuff.HotStuff{}, qmdbEnabled: true, chainConfig: &params.ChainConfig{Performance: &params.PerformanceChainConfig{}}}
	bc.lock.Lock()
	defer bc.lock.Unlock()
	done := make(chan error, 1)
	go func() {
		accounts, err := bc.ReadSpeculativeAppliedParentAccounts(ctx, genesis.Hash(), 0, []types.Address{address})
		if err == nil {
			got := accounts.Accounts[address]
			if accounts.Hash != genesis.Hash() || accounts.Number != 0 || got.Nonce != 3 || got.Balance.Uint64() != 11 {
				err = ErrSpeculativeChildMismatch
			}
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("speculative account read waited for chain mutation lock")
	}
	if _, err := bc.ReadSpeculativeAppliedParentAccounts(ctx, types.Hash{9}, 0, []types.Address{address}); err == nil {
		t.Fatal("different applied parent accepted")
	}
}

func TestSpeculativePlainWorkerRequiresExactAppliedMarker(t *testing.T) {
	db := newRealignTestDB(t)
	ctx := context.Background()
	hash := types.Hash{1}
	if err := db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteQMDBApplied(tx, 4, hash) }); err != nil {
		t.Fatal(err)
	}
	if err := db.View(ctx, func(tx kv.Tx) error {
		ibs := state.New(nil)
		ibs.SetSpeculativePlainReaders(hash, 4)
		if err := verifySpeculativePlainParent(tx, ibs); err != nil {
			t.Fatalf("exact marker rejected: %v", err)
		}
		ibs.SetSpeculativePlainReaders(types.Hash{2}, 4)
		if err := verifySpeculativePlainParent(tx, ibs); err == nil {
			t.Fatal("different hash accepted")
		}
		ibs.SetSpeculativePlainReaders(hash, 5)
		if err := verifySpeculativePlainParent(tx, ibs); err == nil {
			t.Fatal("different generation accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSpeculativeExecutionKeyExcludesOnlyHotStuffExtra(t *testing.T) {
	withdrawals := types.Hash{6}
	blobGas, excessBlobGas := uint64(0), uint64(0)
	beacon := types.Hash{7}
	header := &block.Header{
		ParentHash:       types.Hash{1},
		Coinbase:         types.Address{2},
		Difficulty:       uint256.NewInt(0),
		Number:           uint256.NewInt(2),
		GasLimit:         30_000_000,
		Time:             6,
		BaseFee:          uint256.NewInt(7),
		MixDigest:        types.Hash{3},
		Extra:            []byte{4, 5},
		WithdrawalsHash:  &withdrawals,
		BlobGasUsed:      &blobGas,
		ExcessBlobGas:    &excessBlobGas,
		ParentBeaconRoot: &beacon,
	}
	template := block.NewBlock(header, nil).(*block.Block)
	key, err := speculativeExecutionKey(template)
	if err != nil {
		t.Fatal(err)
	}
	copyTemplate := func() *block.Block {
		raw, err := template.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var copy block.Block
		if err := copy.Unmarshal(raw); err != nil {
			t.Fatal(err)
		}
		return &copy
	}
	qcChanged := copyTemplate()
	qcHeader := qcChanged.Header().(*block.Header)
	qcHeader.Extra = []byte{8, 9, 10}
	qcChanged.WithSeal(qcHeader)
	if bytes.Equal(qcChanged.Header().(*block.Header).Extra, template.Header().(*block.Header).Extra) {
		t.Fatal("Extra mutation did not change actual block")
	}
	if got, err := speculativeExecutionKey(qcChanged); err != nil || !bytes.Equal(got, key) {
		t.Fatalf("Extra-only change changed EVM key: %x, %v", got, err)
	}
	for _, tc := range []struct {
		name   string
		change func(*block.Header)
	}{
		{"parent", func(h *block.Header) { h.ParentHash = types.Hash{9} }},
		{"coinbase", func(h *block.Header) { h.Coinbase = types.Address{9} }},
		{"time", func(h *block.Header) { h.Time++ }},
		{"gas limit", func(h *block.Header) { h.GasLimit++ }},
		{"base fee", func(h *block.Header) { h.BaseFee = uint256.NewInt(8) }},
		{"mix digest", func(h *block.Header) { h.MixDigest = types.Hash{9} }},
		{"number", func(h *block.Header) { h.Number = uint256.NewInt(3) }},
		{"beacon root", func(h *block.Header) { r := types.Hash{9}; h.ParentBeaconRoot = &r }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := copyTemplate()
			before, err := changed.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			header := changed.Header().(*block.Header)
			tc.change(header)
			changed.WithSeal(header)
			after, err := changed.Marshal()
			if err != nil || bytes.Equal(after, before) {
				t.Fatalf("header mutation did not change actual block: %v", err)
			}
			got, err := speculativeExecutionKey(changed)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(got, key) {
				t.Fatal("execution-relevant header change retained EVM key")
			}
		})
	}
}
