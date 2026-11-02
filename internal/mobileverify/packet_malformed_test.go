package mobileverify

import (
	"bytes"
	"testing"
)

// An empty header section decodes as a protobuf with every field absent. Any
// gossip peer can send it, unsigned, and it must be dropped as invalid: the
// receive loop has no recover, so a panic here ends the node.
func TestValidatePacketBytesRejectsEmptyHeader(t *testing.T) {
	packet := append([]byte{'N', '2', 1, 0}, make([]byte, 32)...) // magic, version, block hash
	packet = append(packet, bytes.Repeat([]byte{0}, 12)...)       // header, tx and read log lengths
	if _, _, _, err := ValidatePacketBytes(packet); err == nil {
		t.Fatal("packet with an empty header was accepted")
	}
}
