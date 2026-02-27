package utils

func ToBytes4(x []byte) [4]byte {
	var y [4]byte
	copy(y[:], x)
	return y
}

func ToBytes20(x []byte) [20]byte {
	var y [20]byte
	copy(y[:], x)
	return y
}

func ToBytes32(x []byte) [32]byte {
	var y [32]byte
	copy(y[:], x)
	return y
}

func ToBytes48(x []byte) [48]byte {
	var y [48]byte
	copy(y[:], x)
	return y
}

func ToBytes64(x []byte) [64]byte {
	var y [64]byte
	copy(y[:], x)
	return y
}

func ToBytes96(x []byte) [96]byte {
	var y [96]byte
	copy(y[:], x)
	return y
}
