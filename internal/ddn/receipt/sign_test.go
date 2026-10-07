package receipt

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/n42blockchain/N42/accounts/keystore"
	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

func signedFixture(t *testing.T) (*Signer, d.DecisionRequest, d.DecisionReceipt, uint64) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	s, err := NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	now := uint64(time.Now().UnixMilli())
	req := d.DecisionRequest{ChainID: 94, Version: 1, Task: "node.anomaly", SchemaID: "health-v1", InputHash: chain.Hash{1}, PolicyHash: chain.Hash{2}, Quorum: 1, MaxLatencyMs: 100, MaxCost: "0", Deadline: now + 60000, Nonce: 7, Requester: "did:n42:requester", PrivacyMode: "public"}
	id, _ := req.CanonicalHash()
	r := d.DecisionReceipt{ChainID: 94, Version: 1, RequestID: id, ProviderDID: s.DID(), ModelHash: chain.Hash{3}, InputHash: req.InputHash, PolicyHash: req.PolicyHash, Nonce: req.Nonce, StartedAt: now - 10, CompletedAt: now, LatencyMs: 10, Expiry: now + 1000, Result: d.DecisionResult{Label: "NORMAL", ConfidencePPM: 900000}}
	r, err = s.Sign(r)
	if err != nil {
		t.Fatal(err)
	}
	return s, req, r, now
}
func TestReceiptSignVerifyAndTampering(t *testing.T) {
	s, req, r, now := signedFixture(t)
	if err := Verify(r, req, s.Address(), now); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*d.DecisionReceipt){func(r *d.DecisionReceipt) { r.Result.Label = "NETWORK" }, func(r *d.DecisionReceipt) { r.Nonce++ }, func(r *d.DecisionReceipt) { r.ChainID++ }, func(r *d.DecisionReceipt) { r.InputHash[0]++ }, func(r *d.DecisionReceipt) { r.RequestID[0]++ }, func(r *d.DecisionReceipt) { r.Expiry = now }, func(r *d.DecisionReceipt) { r.ProviderSignature = "0x00" }} {
		c := r
		change(&c)
		if err := Verify(c, req, s.Address(), now); err == nil {
			t.Fatal("tampered receipt accepted")
		}
	}
	if Verify(r, req, chain.Address{1}, now) == nil {
		t.Fatal("untrusted signer accepted")
	}
	s.Close()
	if _, err := s.Sign(r); err == nil {
		t.Fatal("closed signer signed")
	}
}
func TestDedicatedEncryptedKeystore(t *testing.T) {
	key, _ := crypto.GenerateKey()
	data, err := keystore.EncryptKey(&keystore.Key{Id: uuid.New(), Address: crypto.PubkeyToAddress(key.PublicKey), PrivateKey: key}, "test-only", 4096, 1)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "ddn-keystore")
	if err = os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSigner(path, "test-only")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Address() != crypto.PubkeyToAddress(key.PublicKey) {
		t.Fatal("wrong decrypted key")
	}
	if _, err = LoadSigner(path, "bad-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	os.Chmod(path, 0644)
	if _, err = LoadSigner(path, "test-only"); err == nil {
		t.Fatal("public key file accepted")
	}
}

func TestSystem1ReceiptRejectsContradictoryEscalationAnswer(t *testing.T) {
	s, req, r, now := signedFixture(t)
	req.SchemaID = "system1-v1"
	req.PolicyParameters.RequireHuman = true
	r.RequestID, _ = req.CanonicalHash()
	r.Result = d.DecisionResult{Label: "NORMAL", NeedEscalation: true, Answers: []d.QuantizedAnswer{{Kind: 1, ProbabilitiesPPM: []uint32{d.PPM, 0, 0}}, {Kind: 1, ProbabilitiesPPM: []uint32{d.PPM, 0, 0, 0, 0, 0, 0, 0}}, {Kind: 3}}}
	signed, err := s.Sign(r)
	if err != nil {
		t.Fatal(err)
	}
	if Verify(signed, req, s.Address(), now) == nil {
		t.Fatal("signed contradictory escalation accepted")
	}
	r.Result = r.Result.EnforcePolicy(req)
	signed, err = s.Sign(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(signed, req, s.Address(), now); err != nil {
		t.Fatal(err)
	}
}
