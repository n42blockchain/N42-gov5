package hotstuff

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

type certificateSinkFunc func(context.Context, *QuorumCertificate) error

func (f certificateSinkFunc) CommitCertificate(ctx context.Context, qc *QuorumCertificate) error {
	return f(ctx, qc)
}

func TestServiceCommitOutputPreservesFullCertificateBeforeSink(t *testing.T) {
	ctx := context.Background()
	setup := newTestSetup(t, 7)
	raw, err := os.ReadFile("../../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		t.Fatal(err)
	}
	for _, validator := range setup.validators {
		genesis.Config.HotStuff.Validators = append(genesis.Config.HotStuff.Validators, params.HotStuffValidatorConfig{Address: validator.Address.Hex(), BLSKey: "0x" + hex.EncodeToString(validator.PublicKey.Marshal())})
	}
	h := New(genesis.Config.HotStuff, genesis.Config)
	defer h.Close()
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngineFromConfig(); err != nil {
		t.Fatal(err)
	}
	db := memdb.NewTestDB(t)
	if err := db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteCanonicalHash(tx, types.Hash{42}, 0) }); err != nil {
		t.Fatal(err)
	}
	s := NewService(h, nil, db, "unused", "unused")
	defer s.Stop()
	hash := types.Hash{9}
	collector := NewVoteCollector(1, hash, 7)
	for i := 0; i < 5; i++ {
		if err := collector.AddVote(ValidatorIndex(i), setup.keys[i].Sign(CommitSigningMessage(1, hash))); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := collector.BuildQCWithMessage(setup.vs, CommitSigningMessage(1, hash))
	if err != nil {
		t.Fatal(err)
	}
	original := qc.Clone()
	calls := 0
	sink := certificateSinkFunc(func(_ context.Context, got *QuorumCertificate) error {
		calls++
		if !bytes.Equal(got.AggregateSignature, original.AggregateSignature) || len(got.Signers) != 7 || got.SignerCount() != 5 {
			t.Fatal("output lost certificate")
		}
		if err := db.View(ctx, func(tx kv.Tx) error {
			record, err := s.decodeCertificate(tx, hash)
			if err != nil {
				return err
			}
			pending, err := tx.GetOne(modules.HotStuffState, certificateKey(certificatePendingPrefix, hash))
			if err != nil {
				return err
			}
			wal, err := LoadConsensusState(tx)
			if err != nil {
				return err
			}
			if len(pending) != 1 || wal == nil || record.QC.BlockHash != hash {
				t.Fatal("network send preceded durable certificate/pending/WAL")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		got.AggregateSignature[0] ^= 1 // Sink cannot corrupt the saved proof.
		return nil
	})
	if err := s.SetCommitCertificateSink(sink); err != nil {
		t.Fatal(err)
	}
	s.handleBlockCommitted(EngineOutput{Type: OutputBlockCommitted, View: 1, Hash: hash, QC: qc})
	if calls != 1 || !bytes.Equal(qc.AggregateSignature, original.AggregateSignature) {
		t.Fatal("certificate was omitted or mutated")
	}
	pending, err := s.PendingCommitCertificates(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ACK not saved: %v", err)
	}
	if err := db.View(ctx, func(tx kv.Tx) error {
		record, err := s.decodeCertificate(tx, hash)
		if err != nil {
			return err
		}
		if !bytes.Equal(record.QC.AggregateSignature, original.AggregateSignature) {
			t.Fatal("saved proof mutated")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceCertificateSinkRejectsDefaultConfiguration(t *testing.T) {
	s := NewService(New(nil, nil), nil, memdb.NewTestDB(t), "unused", "unused")
	defer s.cancel()
	if err := s.SetCommitCertificateSink(certificateSinkFunc(func(context.Context, *QuorumCertificate) error { return nil })); err == nil {
		t.Fatal("default service enabled performance certificate sink")
	}
}
