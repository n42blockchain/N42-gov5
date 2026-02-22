package encoder

import (
	"errors"
	"fmt"
	"io"
)

// maxVarintLen is the maximum number of bytes in a varint-encoded uint64.
const maxVarintLen = 10

var errExcessMaxLength = fmt.Errorf("provided header exceeds the max varint length of %d bytes", maxVarintLen)

// readVarint reads a varint-encoded uint64 from r. The varint indicates the
// length of the remaining payload in the stream.
func readVarint(r io.Reader) (uint64, error) {
	var (
		buf  [maxVarintLen]byte
		read int
		oneByte [1]byte
	)
	for i := 0; i < maxVarintLen; i++ {
		n, err := r.Read(oneByte[:])
		if err != nil {
			return 0, err
		}
		if n != 1 {
			return 0, errors.New("did not read a byte from stream")
		}
		buf[i] = oneByte[0]
		read++

		// MSB not set means this is the final byte of the varint.
		if oneByte[0]&0x80 == 0 {
			break
		}

		// Still have continuation bits after reading the maximum number of bytes.
		if i+1 >= maxVarintLen {
			return 0, errExcessMaxLength
		}
	}

	vi, n := DecodeVarint(buf[:read])
	if n != read {
		return 0, errors.New("varint did not decode entire byte slice")
	}
	return vi, nil
}

// DecodeVarint reads a varint-encoded integer from buf.
// It returns the decoded value and the number of bytes consumed,
// or (0, 0) if buf is incomplete or the value overflows uint64.
func DecodeVarint(buf []byte) (x uint64, n int) {
	for shift := uint(0); shift < 64; shift += 7 {
		if n >= len(buf) {
			return 0, 0
		}
		b := uint64(buf[n])
		n++
		x |= (b & 0x7F) << shift
		if (b & 0x80) == 0 {
			return x, n
		}
	}
	return 0, 0
}

// EncodeVarint returns the varint encoding of x.
func EncodeVarint(x uint64) []byte {
	var buf [maxVarintLen]byte
	var n int
	for n = 0; x > 127; n++ {
		buf[n] = 0x80 | uint8(x&0x7F)
		x >>= 7
	}
	buf[n] = uint8(x)
	n++
	return buf[:n]
}
