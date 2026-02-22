package utils

// ToBytes4 converts a byte slice to a fixed-size 4-byte array.
// The input is truncated if longer than 4 bytes, or zero-padded if shorter.
func ToBytes4(x []byte) [4]byte {
	var y [4]byte
	copy(y[:], x)
	return y
}

// ToBytes20 converts a byte slice to a fixed-size 20-byte array.
// The input is truncated if longer than 20 bytes, or zero-padded if shorter.
func ToBytes20(x []byte) [20]byte {
	var y [20]byte
	copy(y[:], x)
	return y
}

// ToBytes32 converts a byte slice to a fixed-size 32-byte array.
// The input is truncated if longer than 32 bytes, or zero-padded if shorter.
func ToBytes32(x []byte) [32]byte {
	var y [32]byte
	copy(y[:], x)
	return y
}

// ToBytes48 converts a byte slice to a fixed-size 48-byte array.
// The input is truncated if longer than 48 bytes, or zero-padded if shorter.
func ToBytes48(x []byte) [48]byte {
	var y [48]byte
	copy(y[:], x)
	return y
}

// ToBytes64 converts a byte slice to a fixed-size 64-byte array.
// The input is truncated if longer than 64 bytes, or zero-padded if shorter.
func ToBytes64(x []byte) [64]byte {
	var y [64]byte
	copy(y[:], x)
	return y
}

// ToBytes96 converts a byte slice to a fixed-size 96-byte array.
// The input is truncated if longer than 96 bytes, or zero-padded if shorter.
func ToBytes96(x []byte) [96]byte {
	var y [96]byte
	copy(y[:], x)
	return y
}
