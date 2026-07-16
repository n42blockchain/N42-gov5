// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/lib/bmt"
)

func bmtVerify(root types.Hash, proof *bmt.Proof) bool { return bmt.VerifyProof(root, proof) }

// device is a test mobile: a BLS key with registration helpers.
type device struct {
	sk     common.SecretKey
	pubkey [48]byte
}

func newDevice(t *testing.T) *device {
	t.Helper()
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	d := &device{sk: sk}
	copy(d.pubkey[:], sk.PublicKey().Marshal())
	return d
}

func (d *device) pop() [96]byte {
	var out [96]byte
	copy(out[:], d.sk.Sign(PoPMessage(d.pubkey)).Marshal())
	return out
}

func (d *device) receipt(blockHash types.Hash, number uint64, root types.Hash) *Receipt {
	r := &Receipt{
		BlockHash:            blockHash,
		BlockNumber:          number,
		ComputedReceiptsRoot: root,
		VerifierPubkey:       d.pubkey,
	}
	copy(r.Signature[:], d.sk.Sign(SigningMessage(blockHash, number, root)).Marshal())
	return r
}

func h(b byte) types.Hash { var x types.Hash; x[31] = b; return x }

// registerCommitted registers a device and commits the epoch so it can
// attest, returning its committed index.
func registerCommitted(t *testing.T, reg *Registry, pubkey [48]byte, pop [96]byte) MobileIndex {
	t.Helper()
	if _, _, err := reg.Register(pubkey, pop); err != nil {
		t.Fatalf("register: %v", err)
	}
	reg.CommitEpoch()
	idx, ok := reg.Lookup(pubkey)
	if !ok {
		t.Fatal("device not committed after CommitEpoch")
	}
	return idx
}

func TestRegistryPoPAndIdempotence(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)

	// Registration lands in pending — not committed, cannot attest yet.
	if _, committed, err := reg.Register(d.pubkey, d.pop()); err != nil || committed {
		t.Fatalf("first register = (committed=%v, %v), want (false, nil)", committed, err)
	}
	if reg.PendingCount() != 1 || reg.Count() != 0 {
		t.Fatalf("pending=%d committed=%d, want 1/0", reg.PendingCount(), reg.Count())
	}
	// Re-register while pending is a no-op.
	if _, committed, _ := reg.Register(d.pubkey, d.pop()); committed {
		t.Fatal("re-register reported committed while pending")
	}
	if reg.PendingCount() != 1 {
		t.Fatalf("pending grew on re-register: %d", reg.PendingCount())
	}

	// Commit: the device gets index 0 and a non-empty accumulator root.
	root, n := reg.CommitEpoch()
	if n != 1 || reg.Count() != 1 {
		t.Fatalf("commit n=%d count=%d, want 1/1", n, reg.Count())
	}
	if root == (types.Hash{}) {
		t.Fatal("committed root is empty")
	}
	idx, ok := reg.Lookup(d.pubkey)
	if !ok || idx != 0 {
		t.Fatalf("committed index = (%d, %v), want (0, true)", idx, ok)
	}
	// Re-register a committed key returns its index.
	if ri, committed, _ := reg.Register(d.pubkey, d.pop()); !committed || ri != 0 {
		t.Fatalf("re-register committed = (%d, %v), want (0, true)", ri, committed)
	}

	// Membership proof verifies against the root.
	proof, err := reg.MembershipProof(d.pubkey)
	if err != nil {
		t.Fatalf("membership proof: %v", err)
	}
	if !bmtVerify(reg.Root(), proof) {
		t.Fatal("membership proof does not verify against the committed root")
	}

	// Rogue-key defense: a PoP signed by a DIFFERENT key must be rejected.
	rogue := newDevice(t)
	var stolenPoP [96]byte
	copy(stolenPoP[:], rogue.sk.Sign(PoPMessage(d.pubkey)).Marshal())
	other := newDevice(t)
	if _, _, err := reg.Register(other.pubkey, stolenPoP); err == nil {
		t.Fatal("registration with a foreign PoP accepted")
	}

	// A receipt signature must not double as a PoP (domain separation).
	fresh := newDevice(t)
	freshReceipt := fresh.receipt(h(1), 7, h(2))
	if _, _, err := reg.Register(fresh.pubkey, freshReceipt.Signature); err == nil {
		t.Fatal("receipt signature accepted as PoP — domain separation broken")
	}
}

func TestMaskRoundTripAndCanonicality(t *testing.T) {
	cases := [][]MobileIndex{
		{},
		{0},
		{5},
		{0, 1, 2, 3},
		{3, 100, 101, 4000000},
	}
	for _, in := range cases {
		enc, err := EncodeMask(in)
		if err != nil {
			t.Fatalf("encode %v: %v", in, err)
		}
		out, err := DecodeMask(enc, 4000001)
		if err != nil {
			t.Fatalf("decode %v: %v", in, err)
		}
		if len(out) != len(in) {
			t.Fatalf("round trip %v -> %v", in, out)
		}
		for i := range in {
			if out[i] != in[i] {
				t.Fatalf("round trip %v -> %v", in, out)
			}
		}
	}

	// Non-ascending input must be rejected at encode.
	if _, err := EncodeMask([]MobileIndex{2, 2}); err == nil {
		t.Fatal("duplicate indices encoded")
	}
	if _, err := EncodeMask([]MobileIndex{5, 3}); err == nil {
		t.Fatal("descending indices encoded")
	}

	// Decode hardening: out-of-registry index, implausible count, trailing bytes.
	enc, _ := EncodeMask([]MobileIndex{9})
	if _, err := DecodeMask(enc, 9); err == nil {
		t.Fatal("index beyond registry accepted")
	}
	if _, err := DecodeMask([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x0F}, 10); err == nil {
		t.Fatal("implausible count accepted")
	}
	enc2, _ := EncodeMask([]MobileIndex{1})
	if _, err := DecodeMask(append(enc2, 0x00), 10); err == nil {
		t.Fatal("trailing bytes accepted")
	}
}

// TestCollectorEndToEnd drives the full phase-1 pipeline: register a
// fleet, submit receipts (one divergent minority), close the window,
// and verify both certificates against the registry.
func TestCollectorEndToEnd(t *testing.T) {
	reg := NewRegistry()
	blockHash, number := h(0xAA), uint64(13220145)
	goodRoot, badRoot := h(0x01), h(0x02)

	const fleet = 8
	devices := make([]*device, fleet)
	for i := range devices {
		devices[i] = newDevice(t)
		if _, _, err := reg.Register(devices[i].pubkey, devices[i].pop()); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}
	reg.CommitEpoch() // all devices become committed and can attest

	col := NewCollector(reg, blockHash, number)
	for i, d := range devices {
		root := goodRoot
		if i == fleet-1 {
			root = badRoot // one divergent device
		}
		if _, err := col.Add(d.receipt(blockHash, number, root)); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if col.Size() != fleet {
		t.Fatalf("size = %d, want %d", col.Size(), fleet)
	}

	certs, err := col.Close(1234)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if len(certs) != 2 {
		t.Fatalf("certs = %d, want 2 (majority + divergent)", len(certs))
	}
	// Majority first.
	if certs[0].ReceiptsRoot != goodRoot || certs[1].ReceiptsRoot != badRoot {
		t.Fatalf("cert order wrong: %x, %x", certs[0].ReceiptsRoot[:4], certs[1].ReceiptsRoot[:4])
	}

	maj, err := certs[0].Verify(reg)
	if err != nil {
		t.Fatalf("majority cert verify: %v", err)
	}
	if len(maj) != fleet-1 {
		t.Fatalf("majority cohort = %d, want %d", len(maj), fleet-1)
	}
	div, err := certs[1].Verify(reg)
	if err != nil {
		t.Fatalf("divergent cert verify: %v", err)
	}
	if len(div) != 1 {
		t.Fatalf("divergent cohort = %d, want 1", len(div))
	}

	// Tamper checks: flip the root, reuse the aggregate — must fail.
	tampered := *certs[0]
	tampered.ReceiptsRoot = h(0x03)
	if _, err := tampered.Verify(reg); err == nil {
		t.Fatal("tampered cert verified")
	}
	// Mask tamper: claim a different cohort under the same aggregate.
	tampered2 := *certs[0]
	extra := append([]MobileIndex{}, maj...)
	extra = append(extra, MobileIndex(fleet-1)) // add the divergent device
	tampered2.SignerMask, _ = EncodeMask(extra)
	if _, err := tampered2.Verify(reg); err == nil {
		t.Fatal("mask-tampered cert verified")
	}

	// Window closed: further adds and closes rejected.
	if _, err := col.Add(devices[0].receipt(blockHash, number, goodRoot)); err == nil {
		t.Fatal("add after close accepted")
	}
	if _, err := col.Close(5678); err == nil {
		t.Fatal("double close accepted")
	}
}

func TestCollectorGates(t *testing.T) {
	reg := NewRegistry()
	registered := newDevice(t)
	registerCommitted(t, reg, registered.pubkey, registered.pop())
	col := NewCollector(reg, h(0xBB), 42)

	// Unregistered device rejected even with a valid signature.
	stranger := newDevice(t)
	if _, err := col.Add(stranger.receipt(h(0xBB), 42, h(1))); err == nil {
		t.Fatal("unregistered receipt accepted")
	}
	// Wrong block rejected.
	if _, err := col.Add(registered.receipt(h(0xCC), 42, h(1))); err == nil {
		t.Fatal("wrong-block receipt accepted")
	}
	// Bad signature rejected.
	r := registered.receipt(h(0xBB), 42, h(1))
	r.Signature[10] ^= 0xFF
	if _, err := col.Add(r); err == nil {
		t.Fatal("bad-signature receipt accepted")
	}
	// Dedup: same device twice counts once, latest root wins.
	if _, err := col.Add(registered.receipt(h(0xBB), 42, h(1))); err != nil {
		t.Fatal(err)
	}
	if _, err := col.Add(registered.receipt(h(0xBB), 42, h(2))); err != nil {
		t.Fatal(err)
	}
	if col.Size() != 1 {
		t.Fatalf("size = %d, want 1 after dedup", col.Size())
	}
	certs, err := col.Close(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(certs) != 1 || certs[0].ReceiptsRoot != h(2) {
		t.Fatalf("latest-wins violated: %+v", certs)
	}
}
