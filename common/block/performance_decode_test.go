// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"crypto/ed25519"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/params"
)

func offlinePerformanceFixture(t *testing.T, activation uint64) (*params.ChainConfig, *Block, *transaction.Transaction) {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(params.PerformanceChainID), Consensus: params.HotStuffConsensus, HotStuff: &params.HotStuffConfig{}, StateScheme: "qmdb", Performance: &params.PerformanceChainConfig{Version: params.PerformanceEd25519Version, Mode: params.PerformanceModeIndependent, Role: params.PerformanceRoleLocal, Transactions: params.PerformanceTransactionsEd25519, SignatureRule: params.PerformanceEd25519RuleID, Ed25519ActivationBlock: activation}}
	ns, marker, err := cfg.PerformanceEncodingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Performance.NetworkNamespace = ns
	genesis := NewBlock(&Header{Number: uint256.NewInt(0), Time: params.PerformanceGenesisTime, Extra: marker[:]}, nil).(*Block)
	tx, err := transaction.SignAltTx(&transaction.AltSigTx{ChainID: params.PerformanceChainID, Gas: 21000, To: types.Address{19: 1}}, ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x32}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg, genesis, tx
}
func TestPerformanceOfflineBlockRoundTrip(t *testing.T) {
	cfg, genesis, tx := offlinePerformanceFixture(t, 7)
	blk := NewBlock(&Header{Number: uint256.NewInt(7), ParentHash: genesis.Hash()}, []*transaction.Transaction{tx}).(*Block)
	encoded, err := blk.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded Block
	if err := decoded.UnmarshalPerformanceOffline(encoded, cfg, genesis); err != nil {
		t.Fatal(err)
	}
	reencoded, err := decoded.Marshal()
	if err != nil || !bytes.Equal(reencoded, encoded) || decoded.Hash() != blk.Hash() {
		t.Fatalf("block changed: %v", err)
	}
	if len(decoded.body.Txs) != 1 || decoded.body.Txs[0].Hash() != tx.Hash() {
		t.Fatal("transaction lost")
	}
	if _, err := transaction.VerifyAltSigOffline(decoded.body.Txs[0], params.PerformanceChainID); err != nil {
		t.Fatal(err)
	}
	if err := decoded.Unmarshal(encoded); err == nil {
		t.Fatal("default block decoder accepted type 80")
	}
	if _, _, err := decoded.DecodeRLPReusePool(rlp.NewStream(bytes.NewReader(encoded), uint64(len(encoded))), func(_ types.Hash) *transaction.Transaction { return tx }); err == nil {
		t.Fatal("default pool-reuse decoder accepted type80")
	}
	for _, defaultCfg := range []*params.ChainConfig{nil, params.EthereumMainnetChainConfig} {
		if err := decoded.UnmarshalPerformanceOffline(encoded, defaultCfg, genesis); err == nil {
			t.Fatal("nil/ETH configuration admitted type80")
		}
	}
	if err := decoded.UnmarshalPerformanceOffline(append(encoded, 0), cfg, genesis); err == nil {
		t.Fatal("trailing RLP data accepted")
	}
}
func TestPerformanceOfflineBlockRejectsUnboundMetadataAndActivation(t *testing.T) {
	cfg, genesis, tx := offlinePerformanceFixture(t, 7)
	blk := NewBlock(&Header{Number: uint256.NewInt(7)}, []*transaction.Transaction{tx}).(*Block)
	encoded, err := blk.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var decoded Block
	for _, badGenesis := range []*Block{nil, NewBlock(&Header{Number: uint256.NewInt(0), Time: params.PerformanceGenesisTime, Extra: []byte(params.PerformanceGenesisMarker)}, nil).(*Block), NewBlock(&Header{Number: uint256.NewInt(1), Time: genesis.Time(), Extra: genesis.header.Extra}, nil).(*Block)} {
		if err := decoded.UnmarshalPerformanceOffline(encoded, cfg, badGenesis); err == nil {
			t.Fatal("unbound actual genesis accepted")
		}
	}
	changedCfg, _, _ := offlinePerformanceFixture(t, 8)
	if err := decoded.UnmarshalPerformanceOffline(encoded, changedCfg, genesis); err == nil {
		t.Fatal("changed activation retained same genesis")
	}
	before := NewBlock(&Header{Number: uint256.NewInt(6)}, []*transaction.Transaction{tx}).(*Block)
	beforeBytes, err := before.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.UnmarshalPerformanceOffline(beforeBytes, cfg, genesis); err == nil {
		t.Fatal("pre-activation transaction accepted")
	}
	wrongChain, err := transaction.SignAltTx(&transaction.AltSigTx{ChainID: 94}, ed25519.NewKeyFromSeed(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	wrongRaw, _ := wrongChain.EthEncoded()
	if _, err := transaction.DecodePerformanceTransactionOffline(wrongRaw, cfg, 7); err == nil {
		t.Fatal("wrong chain ID accepted by explicit decoder")
	}
	raw, _ := tx.EthEncoded()
	if _, err := transaction.DecodePerformanceTransactionOffline(raw, cfg, 6); err == nil {
		t.Fatal("raw parser ignored activation")
	}
	cfg.Performance.NetworkNamespace = params.PerformanceNamespace
	if err := decoded.UnmarshalPerformanceOffline(encoded, cfg, genesis); err == nil {
		t.Fatal("namespace mismatch accepted")
	}
}
func TestPerformanceOfflineBlocksKeepETHAndRejectFrames(t *testing.T) {
	cfg, genesis, _ := offlinePerformanceFixture(t, 7)
	legacy := transaction.NewTx(&transaction.LegacyTx{Nonce: 1, Gas: 21000, To: &types.Address{19: 1}})
	blk := NewBlock(&Header{Number: uint256.NewInt(6)}, []*transaction.Transaction{legacy}).(*Block)
	encoded, err := blk.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var defaultBlock, performanceBlock Block
	if err := defaultBlock.Unmarshal(encoded); err != nil {
		t.Fatal(err)
	}
	if err := performanceBlock.UnmarshalPerformanceOffline(encoded, cfg, genesis); err != nil {
		t.Fatal(err)
	}
	got, err := performanceBlock.Marshal()
	if err != nil || !bytes.Equal(got, encoded) {
		t.Fatal("ordinary ETH encoding changed")
	}
	framed := NewBlock(&Header{Number: uint256.NewInt(7)}, nil).(*Block)
	framed.body.FrameHashes = []types.Hash{{1}}
	frameBytes, err := framed.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := performanceBlock.UnmarshalPerformanceOffline(frameBytes, cfg, genesis); err == nil {
		t.Fatal("unfinished config-aware frame path accepted")
	}
}

// Unexecuted WIP regression: a Decoder callback can finish before DecodeBytes
// rejects trailing input. Neither errors nor a later success may retain the
// wrong payload/cache state when a caller reuses an exclusively owned Block.
func TestPerformanceOfflineDecodePublishesOnlyCompleteInput(t *testing.T) {
	cfg, genesis, tx := offlinePerformanceFixture(t, 7)
	candidate := NewBlock(&Header{Number: uint256.NewInt(7), ParentHash: genesis.Hash()}, []*transaction.Transaction{tx}).(*Block)
	encoded, err := candidate.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	oldTx := transaction.NewTx(&transaction.LegacyTx{Nonce: 19, Gas: 21000})
	receiver := NewBlock(&Header{Number: uint256.NewInt(99)}, []*transaction.Transaction{oldTx}).(*Block)
	oldHeader, oldBody, oldHash := receiver.header, receiver.body, receiver.Hash()
	receiver.hash.Store(oldHash)
	receiver.size.Store(uint64(123))
	receiver.decodeReused, receiver.decodeDecoded = 4, 9
	oldEncoding, err := receiver.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	preactivation := NewBlock(&Header{Number: uint256.NewInt(6)}, []*transaction.Transaction{tx}).(*Block)
	beforeBytes, err := preactivation.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append(bytes.Clone(encoded), 0), encoded[:len(encoded)-1], beforeBytes} {
		if err := receiver.UnmarshalPerformanceOffline(bad, cfg, genesis); err == nil {
			t.Fatal("malformed/inactive input accepted")
		}
		if receiver.header != oldHeader || receiver.body != oldBody || receiver.Hash() != oldHash || receiver.hash.Load() != oldHash || receiver.size.Load() != uint64(123) {
			t.Fatal("failed decode changed receiver payload or caches")
		}
		if reused, decoded := receiver.DecodeReuseStats(); reused != 4 || decoded != 9 {
			t.Fatal("failed decode changed statistics")
		}
		got, err := receiver.Marshal()
		if err != nil || !bytes.Equal(got, oldEncoding) {
			t.Fatal("failed decode changed previous wire payload")
		}
	}
	if err := receiver.UnmarshalPerformanceOffline(encoded, cfg, genesis); err != nil {
		t.Fatal(err)
	}
	// Check caches before any derived-hash accessor can populate them.
	if receiver.hash.Load() != nil || receiver.size.Load() != nil {
		t.Fatal("successful reuse retained old atomic caches")
	}
	if receiver.Hash() != candidate.Hash() || receiver.body.Txs[0].Hash() != tx.Hash() {
		t.Fatal("successful reuse retained old payload")
	}
	if reused, decoded := receiver.DecodeReuseStats(); reused != 0 || decoded != 0 {
		t.Fatal("offline decode retained pool reuse statistics")
	}
	var missing *Block
	if err := missing.UnmarshalPerformanceOffline(encoded, cfg, genesis); err == nil {
		t.Fatal("nil receiver accepted")
	}
}

// Unexecuted WIP boundary check: even the explicit v2 parser does not grant
// runtime sender/cache authorization at the activation height.
func TestPerformanceOfflineActivationDoesNotEnableSigner(t *testing.T) {
	cfg, _, tx := offlinePerformanceFixture(t, 0)
	raw, err := tx.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := transaction.DecodePerformanceTransactionOffline(raw, cfg, 0)
	if err != nil {
		t.Fatal(err)
	}
	signer := transaction.MakeSignerWithTimestamp(cfg, big.NewInt(0), params.PerformanceGenesisTime)
	parsed.SetFrom(types.Address{1})
	parsed.CacheSender(signer, types.Address{1})
	parsed.SetSenderMemo(signer, types.Address{1})
	if parsed.From() != nil {
		t.Fatal("offline type accepted From claim")
	}
	if _, ok := transaction.CachedSender(signer, parsed); ok {
		t.Fatal("offline type accepted cached sender")
	}
	if _, err := transaction.Sender(signer, parsed); err == nil {
		t.Fatal("offline activation enabled runtime signer")
	}
	if _, err := transaction.RecoverSenderFromSig(signer, parsed); err == nil {
		t.Fatal("offline activation enabled recovery cache")
	}
}
