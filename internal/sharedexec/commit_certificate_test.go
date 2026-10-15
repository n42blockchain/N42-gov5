package sharedexec

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func commitProof(keys []blscommon.SecretKey, hash types.Hash, count int, prepare bool) *hotstuff.QuorumCertificate {
	message := hotstuff.CommitSigningMessage(1, hash)
	if prepare {
		message = hotstuff.SigningMessage(1, hash)
	}
	qc := &hotstuff.QuorumCertificate{View: 1, BlockHash: hash, Signers: make([]bool, len(keys))}
	var signatures []blscommon.Signature
	for i := 0; i < count; i++ {
		qc.Signers[i] = true
		signatures = append(signatures, keys[i].Sign(message))
	}
	qc.AggregateSignature = bls.AggregateSignatures(signatures).Marshal()
	return qc
}

func TestCertifiedCommitUnixRequiresFiveOfSevenCommitDomain(t *testing.T) {
	ctx := context.Background()
	var keys []blscommon.SecretKey
	chain, public, leader, template, _, _ := unsignedBuildFixtureWithSet(t, 7, func(k []blscommon.SecretKey) { keys = k })
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	result, err := client.Build(ctx, buildRequest(t, server.Identity(), template))
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(result.BlockRLP); err != nil {
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
	request := CommitRequest{Identity: server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: 1}
	valid := commitProof(keys, b.Hash(), 5, false)
	if _, err := client.CommitCertified(ctx, request, valid); err == nil {
		t.Fatal("unexecuted certified block committed")
	}
	imported, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !imported.Durable {
		t.Fatalf("real import: %v", err)
	}
	badSeal := b.Header().(*block.Header)
	badSeal.Extra[len(badSeal.Extra)-1] ^= 1
	badSeal.ResetHashCache()
	if err := public.VerifyPerformanceCommitCertificate(chain, badSeal, commitProof(keys, badSeal.Hash(), 5, false)); err == nil {
		t.Fatal("valid CommitQC bypassed invalid block seal")
	}
	for _, qc := range []*hotstuff.QuorumCertificate{commitProof(keys, b.Hash(), 4, false), commitProof(keys, b.Hash(), 5, true)} {
		if _, err := client.CommitCertified(ctx, request, qc); err == nil {
			t.Fatal("insufficient quorum or Prepare domain committed")
		}
	}
	wrong := request
	wrong.Identity.GenesisHash[0] ^= 1
	if _, err := server.CommitCertified(ctx, CertifiedCommitRequest{CommitRequest: wrong, Certificate: *valid}); err == nil {
		t.Fatal("wrong chain committed")
	}
	if chain.CurrentBlock().Hash() != chain.GenesisBlock().Hash() {
		t.Fatal("rejected proof advanced canonical")
	}
	for range 2 {
		committed, err := client.CommitCertified(ctx, request, valid)
		if err != nil || !committed.Committed {
			t.Fatalf("5/7 commit: %v", err)
		}
	}
	if chain.CurrentBlock().Hash() != b.Hash() {
		t.Fatal("5/7 proof did not advance canonical")
	}
	forged := valid.Clone()
	forged.AggregateSignature[0] ^= 1
	if _, err := client.CommitCertified(ctx, request, &forged); err == nil {
		t.Fatal("duplicate path skipped certificate verification")
	}
	if server.commitOwner != ([32]byte{}) {
		t.Fatal("certificate path acquired owner capability")
	}
}

type dropCertificateAck struct {
	bridge  *CertificateBridge
	dropped bool
}

func (s *dropCertificateAck) CommitCertificate(ctx context.Context, qc *hotstuff.QuorumCertificate) error {
	if err := s.bridge.CommitCertificate(ctx, qc); err != nil {
		return err
	}
	if !s.dropped {
		s.dropped = true
		return errors.New("test lost remote ACK")
	}
	return nil
}

func TestCommitCertificateOutboxRestartsAfterLostUnixAck(t *testing.T) {
	ctx := context.Background()
	var keys []blscommon.SecretKey
	chain, public, leader, template, _, _ := unsignedBuildFixtureWithSet(t, 7, func(k []blscommon.SecretKey) { keys = k })
	server, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, server), server.Identity())
	defer client.Close()
	result, err := client.Build(ctx, buildRequest(t, server.Identity(), template))
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(result.BlockRLP); err != nil {
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
	if result, err := client.Import(ctx, ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw}); err != nil || !result.Durable {
		t.Fatalf("actual import: %v", err)
	}
	// Independent metadata/WAL database: no local StateProcessor or account
	// state is used by the forwarding service. Keep its canonical at genesis.
	local := memdb.New(t.TempDir())
	t.Cleanup(local.Close)
	if err := local.Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, chain.GenesisBlock().(*block.Block)); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, chain.GenesisBlock().Hash(), 0); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, chain.GenesisBlock().Hash())
		if err := rawdb.WriteChainConfig(tx, chain.GenesisBlock().Hash(), chain.Config()); err != nil {
			return err
		}
		return rawdb.WriteBlock(tx, b)
	}); err != nil {
		t.Fatal(err)
	}
	bridge, err := NewCertificateBridge(client, local)
	if err != nil {
		t.Fatal(err)
	}
	service := hotstuff.NewService(public, nil, local, "unused", "unused")
	if err := service.SetCommitCertificateSink(&dropCertificateAck{bridge: bridge}); err != nil {
		t.Fatal(err)
	}
	qc := commitProof(keys, b.Hash(), 5, false)
	if err := service.CommitCertificate(ctx, qc); err == nil {
		t.Fatal("lost ACK reported successful")
	}
	pending, err := service.PendingCommitCertificates(ctx)
	if err != nil || len(pending) != 1 || pending[0].BlockHash != b.Hash() || len(pending[0].AggregateSignature) != 96 {
		t.Fatalf("certificate lost before ACK: %+v %v", pending, err)
	}
	if chain.CurrentBlock().Hash() != b.Hash() {
		t.Fatal("remote commit did not happen before lost ACK")
	}
	service.Stop()
	restarted := hotstuff.NewService(public, nil, local, "unused", "unused")
	defer restarted.Stop()
	if err := restarted.SetCommitCertificateSink(bridge); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ReplayCommitCertificates(ctx); err != nil {
		t.Fatal(err)
	}
	pending, err = restarted.PendingCommitCertificates(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("restart did not clear ACKed proof: %+v %v", pending, err)
	}
	if err := local.View(ctx, func(tx kv.Tx) error {
		if rawdb.ReadHeadBlockHash(tx) != chain.GenesisBlock().Hash() {
			t.Fatal("cross-DB ACK pretended local canonical advanced")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := restarted.CommitCertificate(ctx, qc); err != nil {
		t.Fatal(err)
	}
	if server.ImportCalls() != 1 {
		t.Fatal("QC retry re-executed block")
	}
}
