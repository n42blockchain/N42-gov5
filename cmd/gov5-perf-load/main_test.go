package main

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/params"
)

func TestCorpusRecoversIndependentSendersNoncesAndRecipients(t *testing.T) {
	g := &conf.Genesis{Config: &params.ChainConfig{ChainID: big.NewInt(params.PerformanceChainID), Performance: &params.PerformanceChainConfig{}}, Alloc: make(conf.GenesisAlloc)}
	var keys []string
	for range 3 {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, hex.EncodeToString(crypto.FromECDSA(key)))
		g.Alloc[crypto.PubkeyToAddress(key.PublicKey)] = conf.GenesisAccount{Balance: "0x100000000000000"}
	}
	wires, digest, err := signedCorpus(g, keys, 4)
	if err != nil || len(wires) != 12 || len(digest) != 64 {
		t.Fatalf("corpus %d %s %v", len(wires), digest, err)
	}
	wires2, digest2, err := signedCorpus(g, keys, 4)
	if err != nil || digest != digest2 || len(wires2) != len(wires) {
		t.Fatal("corpus is not deterministic")
	}
	nonces := map[types.Address]uint64{}
	recipients := map[types.Address]types.Address{}
	hashes := map[types.Hash]bool{}
	signer := transaction.NewLondonSigner(g.Config.ChainID)
	for _, wire := range wires {
		tx, err := transaction.DecodeEthereumTransaction(wire)
		if err != nil {
			t.Fatal(err)
		}
		from, err := transaction.RecoverSenderFromSig(signer, tx)
		if err != nil {
			t.Fatal(err)
		}
		if tx.Nonce() != nonces[from] || tx.Gas() != 21000 || tx.To() == nil {
			t.Fatal("nonce/transfer layout")
		}
		nonces[from]++
		to := *tx.To()
		if old, ok := recipients[from]; ok && old != to {
			t.Fatal("recipient changed per sender")
		}
		recipients[from] = to
		if hashes[tx.Hash()] {
			t.Fatal("duplicate signed transaction")
		}
		hashes[tx.Hash()] = true
	}
	if len(nonces) != 3 || len(recipients) != 3 {
		t.Fatal("sender collapsed")
	}
	seen := map[types.Address]bool{}
	for from, to := range recipients {
		if nonces[from] != 4 || seen[to] {
			t.Fatal("nonce chain or recipient hotspot")
		}
		seen[to] = true
		if _, ok := g.Alloc[to]; ok {
			t.Fatal("recipient collides with sender")
		}
	}
	if _, _, err := signedCorpus(g, append(keys, keys[0]), 4); err == nil {
		t.Fatal("duplicate keys")
	}
	unfunded, err := crypto.HexToECDSA(keys[0])
	if err != nil {
		t.Fatal(err)
	}
	delete(g.Alloc, crypto.PubkeyToAddress(unfunded.PublicKey))
	if _, _, err := signedCorpus(g, keys, 4); err == nil {
		t.Fatal("unfunded sender")
	}
}

type feedClient struct {
	outcomes    []error
	calls       int
	statusError error
}

func (c *feedClient) Status(context.Context) (sharedvalidator.StatusResult, error) {
	return sharedvalidator.StatusResult{}, c.statusError
}
func (c *feedClient) Submit(context.Context, sharedvalidator.CanonicalProgress, [][]byte) (sharedvalidator.TransactionsResult, error) {
	index := c.calls
	c.calls++
	if index < len(c.outcomes) {
		return sharedvalidator.TransactionsResult{}, c.outcomes[index]
	}
	return sharedvalidator.TransactionsResult{}, nil
}

func TestFeedOnlyRetriesExplicitHeadRejection(t *testing.T) {
	corpus := [][]byte{{1}, {2}, {3}, {4}}
	client := &feedClient{outcomes: []error{sharedvalidator.ErrAdmissionHeadChanged, nil}}
	result := feed(context.Background(), client, corpus, 4)
	if client.calls != 2 || result.Accepted != 4 || result.HeadRejections != 1 || result.Retries != 1 || !result.SupplyExhausted || len(result.Submissions) != 2 {
		t.Fatalf("explicit rejection %+v calls%d", result, client.calls)
	}
	unknown := errors.New("connection closed after request; acceptance unknown")
	client = &feedClient{outcomes: []error{nil, unknown}}
	result = feed(context.Background(), client, corpus, 2)
	if client.calls != 2 || result.Accepted != 2 || result.Error == "" || result.Retries != 0 || result.SupplyExhausted {
		t.Fatal("unknown acceptance was retried or counted")
	}
	client = &feedClient{statusError: unknown}
	result = feed(context.Background(), client, corpus, 4)
	if client.calls != 0 || result.Error == "" {
		t.Fatal("failed status submitted a batch")
	}
	client = &feedClient{outcomes: make([]error, 10)}
	for i := range client.outcomes {
		client.outcomes[i] = sharedvalidator.ErrAdmissionHeadChanged
	}
	result = feed(context.Background(), client, corpus, 4)
	if client.calls != 10 || result.HeadRejections != 10 || result.Accepted != 0 || result.Error == "" {
		t.Fatal("head rejection retry budget was not bounded")
	}
}
