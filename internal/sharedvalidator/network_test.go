package sharedvalidator

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestMetadataFramesBoundLengthBeforeBody(t *testing.T) {
	var packet bytes.Buffer
	if err := writeFrame(&packet, []byte("body"), 4); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&packet, 4)
	if err != nil || string(got) != "body" {
		t.Fatalf("frame roundtrip: %q %v", got, err)
	}
	if err := writeFrame(&packet, []byte("oversize"), 4); err == nil {
		t.Fatal("oversized output accepted")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], 0xffffffff)
	if _, err := readFrame(bytes.NewReader(size[:]), 4); err == nil || err.Error() != "metadata frame exceeds limit" {
		t.Fatalf("oversized input length not rejected before body: %v", err)
	}
	if _, err := readFrame(bytes.NewReader([]byte{0, 0}), 4); err == nil {
		t.Fatal("truncated length accepted")
	}
	if _, err := readFrame(bytes.NewReader([]byte{0, 0, 0, 4, 1}), 4); err == nil {
		t.Fatal("truncated body accepted")
	}
}
