package receipt

import (
	"bytes"
	"math/big"
	"testing"

	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

// Fixed values were produced by the actual n42-26 relay's quote_digest,
// result_digest and answer_hash via a separate, read-only Cargo harness.
func TestN42RustInteropVectors(t *testing.T) {
	address := func(b byte) chain.Address { return chain.BytesToAddress(bytes.Repeat([]byte{b}, 20)) }
	hash := func(b byte) chain.Hash { return chain.BytesToHash(bytes.Repeat([]byte{b}, 32)) }
	q := QuoteFields{ChainID: 94, Hub: address(1), Requester: address(2), RefundTo: address(3), Consumer: address(4), TemplateID: 1, InputHash: hash(5), Deadline: 123, SignerVersion: 1, Fee: big.NewInt(1000), QuoteExpiry: 100}
	r := ResultFields{ChainID: 94, Hub: address(0x11), RequestID: big.NewInt(7), AnswerHash: hash(0x22), EvidenceHash: hash(0x33), ModelHash: hash(0x44), SignerVersion: 1}
	qh, err := QuoteDigest(q)
	if err != nil {
		t.Fatal(err)
	}
	rh, err := ResultDigest(r)
	if err != nil {
		t.Fatal(err)
	}
	ah, err := AnswerHash([]d.QuantizedAnswer{{Kind: 1, ConfidencePPM: 800000, ProbabilitiesPPM: []uint32{800000, 200000}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		got  chain.Hash
		want string
	}{{qh, "0x1e2011148b50b7b8f8e19515609140fdc1b69922dd38b1032d68bc24d9ff9f65"}, {rh, "0x5c78b20513f8c8ab6dee54df483d332bcfa453e1d35c4b59cc2142a36ffb4707"}, {ah, "0x060089bb977db0580448c56f786865477163215f7e9372372d12482d70803bb1"}} {
		if v.got != chain.HexToHash(v.want) {
			t.Fatalf("Rust interop mismatch: got %s want %s", v.got, v.want)
		}
	}
	key, _ := crypto.GenerateKey()
	s, _ := NewSigner(key)
	defer s.Close()
	sig, err := s.SignResult(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := RecoverEIP712(rh, sig)
	if err != nil || got != s.Address() {
		t.Fatal("EIP-712 signature recovery failed")
	}
	r.ChainID++
	other, _ := ResultDigest(r)
	if other == rh {
		t.Fatal("domain failed to bind chain")
	}
	got, _ = RecoverEIP712(other, sig)
	if got == s.Address() {
		t.Fatal("signature reused across chains")
	}
	q.RefundTo = address(6)
	changed, _ := QuoteDigest(q)
	if changed == qh {
		t.Fatal("refund address not bound")
	}
	q.Fee = new(big.Int).Lsh(big.NewInt(1), 256)
	if _, err := QuoteDigest(q); err == nil {
		t.Fatal("overflow accepted")
	}
}
