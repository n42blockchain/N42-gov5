// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package syncwire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/golang/snappy"
)

// MaxErrorMessage bounds the UTF-8 message of an error chunk.
const MaxErrorMessage = 256

// AppendFramed appends uvarint(len(payload)) ‖ snappy-framed(payload).
func AppendFramed(dst, payload []byte) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(payload)))
	var buf bytes.Buffer
	w := snappy.NewBufferedWriter(&buf)
	_, _ = w.Write(payload)
	_ = w.Close()
	return append(dst, buf.Bytes()...)
}

// WriteFramed writes uvarint(len(payload)) ‖ snappy-framed(payload).
func WriteFramed(w io.Writer, payload []byte) error {
	_, err := w.Write(AppendFramed(nil, payload))
	return err
}

// ErrTooLarge reports a declared length above the reader's maximum.
var ErrTooLarge = errors.New("syncwire: declared length above the cap")

// ReadFramed reads uvarint(len) ‖ snappy-framed(payload) and refuses a
// declared length above max before allocating.
func ReadFramed(r io.Reader, max uint64) ([]byte, error) {
	n, err := readUvarint(r)
	if err != nil {
		return nil, err
	}
	if n > max {
		return nil, fmt.Errorf("%w: %d > %d", ErrTooLarge, n, max)
	}
	limit := int64(snappy.MaxEncodedLen(int(n))) + 64
	sr := snappy.NewReader(io.LimitReader(r, limit))
	out := make([]byte, n)
	if _, err := io.ReadFull(sr, out); err != nil {
		return nil, fmt.Errorf("syncwire: framed payload: %w", err)
	}
	return out, nil
}

func readUvarint(r io.Reader) (uint64, error) {
	var x uint64
	var b [1]byte
	for i := 0; i < binary.MaxVarintLen64; i++ {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			if i > 0 && errors.Is(err, io.EOF) {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		if b[0] < 0x80 {
			if i == binary.MaxVarintLen64-1 && b[0] > 1 {
				return 0, errors.New("syncwire: uvarint overflow")
			}
			return x | uint64(b[0])<<(7*i), nil
		}
		x |= uint64(b[0]&0x7f) << (7 * i)
	}
	return 0, errors.New("syncwire: uvarint overflow")
}

// AppendChunk appends one response chunk: code ‖ [digest] ‖ framed payload.
// A nil digest writes none (every chunk except blocks and headers of
// bodies_by_range/2).
func AppendChunk(dst []byte, code byte, digest *[4]byte, payload []byte) []byte {
	dst = append(dst, code)
	if digest != nil {
		dst = append(dst, digest[:]...)
	}
	return AppendFramed(dst, payload)
}

// AppendError appends an error chunk code ‖ framed(utf8 message).
func AppendError(dst []byte, code byte, msg string) []byte {
	if len(msg) > MaxErrorMessage {
		msg = msg[:MaxErrorMessage]
	}
	return AppendChunk(dst, code, nil, []byte(msg))
}

// Chunk is one response chunk as read by ReadChunk.
type Chunk struct {
	Code    byte
	Digest  [4]byte // only when the reader asked for one (SUCCESS chunks)
	Payload []byte  // the decoded payload; the message of an error chunk
}

// ReadChunk reads one chunk. withDigest says SUCCESS chunks carry the 4
// context bytes. max bounds a SUCCESS payload; END payloads must be 17 bytes
// and error messages are bounded by MaxErrorMessage. io.EOF is returned
// only when the stream ends cleanly before the code byte.
func ReadChunk(r io.Reader, withDigest bool, max uint64) (Chunk, error) {
	var c Chunk
	var code [1]byte
	if _, err := io.ReadFull(r, code[:]); err != nil {
		return c, err
	}
	c.Code = code[0]
	switch {
	case c.Code == CodeSuccess:
		if withDigest {
			if _, err := io.ReadFull(r, c.Digest[:]); err != nil {
				return c, fmt.Errorf("syncwire: chunk digest: %w", noEOF(err))
			}
		}
		p, err := ReadFramed(r, max)
		c.Payload = p
		return c, noEOFErr(err)
	case c.Code == CodeEnd:
		p, err := ReadFramed(r, EndLen)
		c.Payload = p
		return c, noEOFErr(err)
	default:
		// The message is optional on the wire; the code is the answer.
		p, err := ReadFramed(r, MaxErrorMessage)
		if err == nil {
			c.Payload = p
		}
		return c, nil
	}
}

func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

func noEOFErr(err error) error {
	if err == nil {
		return nil
	}
	return noEOF(err)
}

// CodeError is a non-SUCCESS, non-END chunk received from a server.
type CodeError struct {
	Code byte
	Msg  string
}

func (e *CodeError) Error() string {
	return fmt.Sprintf("remote returned code %#02x: %s", e.Code, e.Msg)
}
