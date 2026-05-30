package stateless

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func sampleBundle() *StatelessBundle {
	codeA := []byte{0x60, 0x01, 0x60, 0x02}
	chA := types.BytesToHash(keccak(codeA))
	chB := types.BytesToHash([]byte{0xbe, 0xef}) // old code, not shipped

	c0 := AccountChange{
		AddrHash:     types.BytesToHash([]byte{0x11}),
		Nonce:        7,
		CodeHash:     chA[:],
		StorageRoot:  keccak([]byte{0x80}),
		StorageProof: [][]byte{{0xaa, 0xbb}, {0xcc}},
		Storage:      []StorageChange{{SlotHash: types.BytesToHash([]byte{0x22}), Value: []byte{0x05}}},
	}
	c0.Balance.SetUint64(1234567890)
	c1 := AccountChange{
		AddrHash: types.BytesToHash([]byte{0x33}),
		Nonce:    0,
		CodeHash: chB[:], // references old code not in NewCode
		Deleted:  false,
	}
	c1.Balance.Set(uint256.NewInt(42))
	c2 := AccountChange{AddrHash: types.BytesToHash([]byte{0x44}), Deleted: true}

	return &StatelessBundle{
		Number:  25208529,
		Header:  []byte("header-rlp-bytes"),
		Body:    []byte("body-rlp-bytes"),
		Witness: []byte{0x01, 0x20, 0xff},
		NewCode: [][]byte{codeA},
		Proof: &BlockProof{
			Number:       25208529,
			AccountProof: [][]byte{{0x01, 0x02}, {0x03}},
			Changes:      []AccountChange{c0, c1, c2},
		},
	}
}

func TestBundleRoundTrip(t *testing.T) {
	b := sampleBundle()
	dec, err := DecodeBundle(b.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dec.Number != b.Number || !bytes.Equal(dec.Header, b.Header) ||
		!bytes.Equal(dec.Body, b.Body) || !bytes.Equal(dec.Witness, b.Witness) {
		t.Fatal("envelope fields mismatch")
	}
	if len(dec.NewCode) != 1 || !bytes.Equal(dec.NewCode[0], b.NewCode[0]) {
		t.Fatal("NewCode mismatch")
	}
	if dec.Proof == nil || dec.Proof.Number != b.Proof.Number || len(dec.Proof.Changes) != 3 {
		t.Fatalf("proof shape mismatch")
	}
	// Re-encode the decoded proof must be byte-identical (canonical codec).
	if !bytes.Equal(EncodeBlockProof(dec.Proof), EncodeBlockProof(b.Proof)) {
		t.Fatal("BlockProof re-encode not identical")
	}
	// Spot-check a change's fields survived.
	g := dec.Proof.Changes[0]
	if g.Nonce != 7 || g.Balance.Uint64() != 1234567890 ||
		!bytes.Equal(g.CodeHash, b.Proof.Changes[0].CodeHash) ||
		len(g.StorageProof) != 2 || len(g.Storage) != 1 || g.Storage[0].Value[0] != 0x05 {
		t.Fatalf("change[0] fields mismatch: %+v", g)
	}
	if !dec.Proof.Changes[2].Deleted {
		t.Fatal("deleted flag lost")
	}
}

func TestMissingCodeHashes(t *testing.T) {
	b := sampleBundle()
	chB := types.BytesToHash([]byte{0xbe, 0xef})

	// Nothing cached: only chB is missing (chA shipped in NewCode, chC empty/deleted).
	miss := b.MissingCodeHashes(func(types.Hash) bool { return false })
	if len(miss) != 1 || miss[0] != chB {
		t.Fatalf("want [chB], got %v", miss)
	}
	// chB already cached -> nothing missing.
	miss = b.MissingCodeHashes(func(h types.Hash) bool { return h == chB })
	if len(miss) != 0 {
		t.Fatalf("want none, got %v", miss)
	}
}

func TestVerifyCodeResponse(t *testing.T) {
	code := []byte{0xde, 0xad, 0xbe, 0xef}
	h := types.BytesToHash(keccak(code))
	req := &CodeRequest{Hashes: []types.Hash{h}}

	got, err := VerifyCodeResponse(req, &CodeResponse{Codes: [][]byte{code}})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !bytes.Equal(got[h], code) {
		t.Fatal("verified code mismatch")
	}
	// A substituted blob (not matching any requested hash) must be rejected.
	if _, err := VerifyCodeResponse(req, &CodeResponse{Codes: [][]byte{{0x00}}}); err == nil {
		t.Fatal("expected rejection of unrequested code")
	}
}
