package hotstuff

import (
	"encoding/hex"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestPublicVerifierNeedsNoKeyAndChecksCertificates(t *testing.T) {
	setup := newTestSetup(t, 4)
	cfg := &params.HotStuffConfig{BaseTimeout: 6000, MaxTimeout: 30000}
	for _, v := range setup.validators {
		cfg.Validators = append(cfg.Validators, params.HotStuffValidatorConfig{Address: v.Address.Hex(), BLSKey: "0x" + hex.EncodeToString(v.PublicKey.Marshal())})
	}
	h := New(cfg, nil)
	defer h.Close()
	if err := h.InitVerifierFromConfig(); err != nil {
		t.Fatal(err)
	}
	_, key := h.SignerKey()
	if key != nil || h.Engine() == nil || h.Engine().MyIndex() != NonMemberIndex {
		t.Fatal("public verifier acquired signing authority")
	}
	blockHash := types.Hash{1}
	votes := NewVoteCollector(1, blockHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		if err := votes.AddVote(ValidatorIndex(i), setup.keys[i].Sign(SigningMessage(1, blockHash))); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := votes.BuildQC(setup.vs)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Engine().verifyQCAnyDomain(qc); err != nil {
		t.Fatalf("public QC verification failed: %v", err)
	}
	qc.BlockHash[31] ^= 1
	if err := h.Engine().verifyQCAnyDomain(qc); err == nil {
		t.Fatal("public verifier accepted forged certificate")
	}
}

func TestSigningEngineStillRequiresAuthorize(t *testing.T) {
	setup := newTestSetup(t, 4)
	h := New(&params.HotStuffConfig{BaseTimeout: 6000, MaxTimeout: 30000}, nil)
	defer h.Close()
	if err := h.InitEngine(setup.validators, setup.f); err == nil {
		t.Fatal("normal engine no longer requires signing authorization")
	}
}
