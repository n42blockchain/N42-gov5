package trie

// HexToCompact translates hex-nibble notation (one nibble per byte,
// optional 0x10 terminator) into the standard MPT compact encoding
// (two nibbles per byte plus a one-byte type/odd-length prefix).
//
// Mirrors the canonical implementation in lib/trie/encoding.go but
// kept here so the lib/commitment/trie package has no dependency on
// the rest of the trie infrastructure.
func HexToCompact(hex []byte) []byte {
	terminator := byte(0)
	if hasTerm(hex) {
		terminator = 1
		hex = hex[:len(hex)-1]
	}
	buf := make([]byte, len(hex)/2+1)
	buf[0] = terminator << 5 // flag byte: bit 5 = terminator
	if len(hex)&1 == 1 {
		buf[0] |= 1 << 4    // odd flag
		buf[0] |= hex[0]    // odd nibble lives in the low nibble of the flag byte
		hex = hex[1:]
	}
	decodeNibbles(hex, buf[1:])
	return buf
}

// hasTerm reports whether the hex slice ends with the 0x10 terminator.
func hasTerm(hex []byte) bool {
	return len(hex) > 0 && hex[len(hex)-1] == 16
}

// decodeNibbles packs two-nibble pairs into bytes.
func decodeNibbles(hex []byte, bytes []byte) {
	for i := 0; i < len(hex); i += 2 {
		bytes[i/2] = (hex[i] << 4) | hex[i+1]
	}
}
