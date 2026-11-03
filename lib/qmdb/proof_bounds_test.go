package qmdb

import (
	"encoding/binary"
	"testing"
)

func TestProofRejectsRelabelledSlot(t *testing.T) {
	for _, count := range []int{1, TwigSize + 1} {
		tr := New()
		key := Hash{0xAB}
		tr.Set(key, []byte{7})
		for i := 1; i < count; i++ {
			k := Hash{byte(i), byte(i >> 8), 0xCC}
			tr.Set(k, []byte{1})
		}
		root := tr.Root()
		p, ok := tr.GetProof(key)
		if !ok || !VerifyProof(root, p) {
			t.Fatal("valid fixture proof failed")
		}
		// None of the authenticated path bits change. Only a bit ABOVE the
		// upper tree changes, claiming the leaf lives outside the tree.
		p.Slot += uint64(TwigSize) << len(p.UpperPath)
		if VerifyProof(root, p) || VerifyEncodedProof(root, p.Marshal()) {
			t.Fatalf("accepted relabelled slot %d with upper depth %d", p.Slot, len(p.UpperPath))
		}
	}
}

func TestProofCodecRejectsTrailingBytes(t *testing.T) {
	tr := New()
	key := Hash{0x42}
	tr.Set(key, []byte{1})
	p, _ := tr.GetProof(key)
	blob := append(p.Marshal(), 0x99)
	if _, err := UnmarshalProof(blob); err == nil {
		t.Fatal("accepted proof with trailing bytes rejected by n42-rs")
	}
}

func TestProofCodecRejectsOversizedUpperPath(t *testing.T) {
	p := &Proof{UpperPath: make([]Hash, 65)}
	if _, err := UnmarshalProof(p.Marshal()); err == nil {
		t.Fatal("accepted upper path longer than n42-rs's 64-level limit")
	}
}

func TestProofCodecRejectsOverflowingValueLength(t *testing.T) {
	blob := (&Proof{}).Marshal()
	binary.LittleEndian.PutUint32(blob[len(blob)-4:], ^uint32(0))
	if _, err := UnmarshalProof(blob); err == nil {
		t.Fatal("accepted a value length exceeding the remaining bytes")
	}
}

func TestEncodedProofBindsRequestedKey(t *testing.T) {
	tr := New()
	key := Hash{0x42}
	tr.Set(key, []byte{1})
	root := tr.Root()
	p, _ := tr.GetProof(key)
	if !VerifyEncodedProofForKey(root, key, p.Marshal()) {
		t.Fatal("rejected requested key's valid proof")
	}
	if VerifyEncodedProofForKey(root, Hash{0x99}, p.Marshal()) {
		t.Fatal("accepted another key's proof as the answer")
	}
	if VerifyProof(root, nil) || VerifyEncodedProofForKey(root, key, nil) {
		t.Fatal("accepted an absent proof")
	}
}
