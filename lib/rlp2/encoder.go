package rlp2

import "golang.org/x/exp/constraints"

type EncoderFunc = func(i *Encoder) *Encoder

type Encoder struct {
	buf []byte
}

func NewEncoder(buf []byte) *Encoder {
	return &Encoder{
		buf: buf,
	}
}

// Buffer returns the underlying buffer
func (e *Encoder) Buffer() []byte {
	return e.buf
}

func (e *Encoder) Byte(p byte) *Encoder {
	e.buf = append(e.buf, p)
	return e
}

func (e *Encoder) Bytes(p []byte) *Encoder {
	e.buf = append(e.buf, p...)
	return e
}

// Str writes a string with the correct prefix based on length.
func (e *Encoder) Str(str []byte) *Encoder {
	if len(str) > 55 {
		return e.LongString(str)
	}
	return e.ShortString(str)
}

// ShortString encodes a string assumed to be less than 56 bytes long.
func (e *Encoder) ShortString(str []byte) *Encoder {
	return e.Byte(TokenShortBlob.Plus(byte(len(str)))).Bytes(str)
}

// LongString encodes a string assumed to be greater than 55 bytes long.
func (e *Encoder) LongString(str []byte) *Encoder {
	e.Byte(byte(TokenLongBlob))
	n := putUint(e, len(str))
	e.buf[len(e.buf)-(int(n)+1)] += n
	e.buf = append(e.buf, str...)
	return e
}

// List encodes the given items as an RLP list with automatic prefix selection.
func (e *Encoder) List(items ...EncoderFunc) *Encoder {
	return e.writeList(true, items...)
}

// ShortList is an alias for List.
func (e *Encoder) ShortList(items ...EncoderFunc) *Encoder {
	return e.writeList(true, items...)
}

// LongList encodes a list assumed to have a payload greater than 55 bytes.
func (e *Encoder) LongList(items ...EncoderFunc) *Encoder {
	return e.writeList(false, items...)
}

// writeList writes items as an RLP list. When validate is true and the encoded
// payload fits in 55 bytes, it rewrites the header as a short list prefix.
func (e *Encoder) writeList(validate bool, items ...EncoderFunc) *Encoder {
	e = e.Byte(byte(TokenLongList))
	e = e.Bytes(make([]byte, 8))
	startLength := len(e.buf)
	for _, v := range items {
		e = v(e)
	}
	dataSize := len(e.buf) - startLength
	if dataSize <= 55 && validate {
		e.buf[startLength-8-1] = TokenShortList.Plus(byte(dataSize))
		copy(e.buf[startLength-8:], e.buf[startLength:startLength+dataSize])
		e.buf = e.buf[:startLength+dataSize-8]
		return e
	}
	enc := NewEncoder(e.buf[startLength-8:])
	n := putUint(enc, dataSize)
	e.buf[startLength-8-1] += n
	if shift := int(8 - n); shift > 0 {
		copy(e.buf[startLength-shift:], e.buf[startLength:startLength+dataSize])
		e.buf = e.buf[:startLength-shift+dataSize]
	}
	return e
}

func putUint[T constraints.Integer](e *Encoder, t T) (size byte) {
	i := uint64(t)
	switch {
	case i < (1 << 8):
		e.buf = append(e.buf, byte(i))
		return 1
	case i < (1 << 16):
		e.buf = append(e.buf,
			byte(i>>8),
			byte(i),
		)
		return 2
	case i < (1 << 24):
		e.buf = append(e.buf,
			byte(i>>16),
			byte(i>>8),
			byte(i),
		)
		return 3
	case i < (1 << 32):
		e.buf = append(e.buf,
			byte(i>>24),
			byte(i>>16),
			byte(i>>8),
			byte(i),
		)
		return 4
	case i < (1 << 40):
		e.buf = append(e.buf,
			byte(i>>32),
			byte(i>>24),
			byte(i>>16),
			byte(i>>8),
			byte(i),
		)
		return 5
	case i < (1 << 48):
		e.buf = append(e.buf,
			byte(i>>40),
			byte(i>>32),
			byte(i>>24),
			byte(i>>16),
			byte(i>>8),
			byte(i),
		)
		return 6
	case i < (1 << 56):
		e.buf = append(e.buf,
			byte(i>>48),
			byte(i>>40),
			byte(i>>32),
			byte(i>>24),
			byte(i>>16),
			byte(i>>8),
			byte(i),
		)
		return 7
	default:
		e.buf = append(e.buf,
			byte(i>>56),
			byte(i>>48),
			byte(i>>40),
			byte(i>>32),
			byte(i>>24),
			byte(i>>16),
			byte(i>>8),
			byte(i),
		)
		return 8
	}
}
