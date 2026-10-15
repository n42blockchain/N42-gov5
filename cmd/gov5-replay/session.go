package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

const supplyWindow = 32

// supplyCredits is shared by every shard, rather than granting each connection
// the same snapshot of available capacity. Reservations include unacknowledged
// frames; the receiver additionally limits its independently owned buffers.
type supplyCredits struct {
	mu                         sync.Mutex
	changed                    chan struct{}
	maxTx, maxBytes, tx, bytes uint64
}

func newSupplyCredits(tx, bytes uint64) *supplyCredits {
	return &supplyCredits{changed: make(chan struct{}), maxTx: tx, maxBytes: bytes}
}

func (c *supplyCredits) reserve(ctx context.Context, tx, bytes uint64) error {
	if tx == 0 || tx > c.maxTx || bytes == 0 || bytes > c.maxBytes {
		return errors.New("frame exceeds global credit budget")
	}
	for {
		c.mu.Lock()
		if tx <= c.maxTx-c.tx && bytes <= c.maxBytes-c.bytes {
			c.tx += tx
			c.bytes += bytes
			c.mu.Unlock()
			return nil
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (c *supplyCredits) release(tx, bytes uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if tx > c.tx || bytes > c.bytes {
		panic("supply credit accounting underflow")
	}
	c.tx -= tx
	c.bytes -= bytes
	close(c.changed)
	c.changed = make(chan struct{})
}

func (c *supplyCredits) used() (uint64, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tx, c.bytes
}

func (c *supplyCredits) tryReserve(tx, bytes uint64) (bool, error) {
	if tx == 0 || tx > c.maxTx || bytes == 0 || bytes > c.maxBytes {
		return false, errors.New("frame exceeds global credit budget")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if tx > c.maxTx-c.tx || bytes > c.maxBytes-c.bytes {
		return false, nil
	}
	c.tx += tx
	c.bytes += bytes
	return true, nil
}

type supplyManifest struct {
	sharedexec.CorpusManifest
	Digest types.Hash
}

func parseSupplyManifest(raw []byte) (supplyManifest, error) {
	manifest, err := sharedexec.DecodeCorpusManifest(raw)
	if err != nil {
		return supplyManifest{}, err
	}
	// Pin the exact finalized document, including every shard hash. CorpusID
	// alone identifies only the descriptor and cannot authenticate the payload.
	return supplyManifest{CorpusManifest: manifest, Digest: types.Hash(sha256.Sum256(raw))}, nil
}

// Each connection is pinned to one manifest and one shard; reconnecting after
// an unknown ACK is unsupported. A new process epoch cannot resume old credits.
func writeSupplyHello(w io.Writer, digest types.Hash, shard uint16) error {
	var raw [38]byte
	copy(raw[:4], "GSH1")
	copy(raw[4:36], digest[:])
	binary.LittleEndian.PutUint16(raw[36:], shard)
	return writeSupplyBytes(w, raw[:])
}

func readSupplyHello(r io.Reader, manifest supplyManifest) (uint16, error) {
	var raw [38]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return 0, err
	}
	var digest types.Hash
	copy(digest[:], raw[4:36])
	shard := binary.LittleEndian.Uint16(raw[36:])
	if string(raw[:4]) != "GSH1" || digest != manifest.Digest || shard >= manifest.Descriptor.ShardCount {
		return 0, errors.New("supply manifest/shard handshake mismatch")
	}
	return shard, nil
}

func writeSupplyWelcome(w io.Writer, digest types.Hash, epoch [16]byte) error {
	var raw [52]byte
	copy(raw[:4], "GSW1")
	copy(raw[4:36], digest[:])
	copy(raw[36:], epoch[:])
	return writeSupplyBytes(w, raw[:])
}

func readSupplyWelcome(r io.Reader, digest types.Hash) ([16]byte, error) {
	var raw [52]byte
	var epoch [16]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return epoch, err
	}
	var actual types.Hash
	copy(actual[:], raw[4:36])
	copy(epoch[:], raw[36:])
	if string(raw[:4]) != "GSW1" || actual != digest || epoch == ([16]byte{}) {
		return [16]byte{}, errors.New("supply manifest/epoch welcome mismatch")
	}
	return epoch, nil
}

// ACKs acknowledge provisional transport consumption, never signature validity
// or chain admission. Final shard integrity must pass before qualification.
type supplyAck struct {
	Epoch               [16]byte
	Shard               uint16
	Sequence            uint64
	PayloadHash         types.Hash
	New, Duplicate      uint16
	QueueTx, QueueBytes uint64
	Final               bool
}

func writeSupplyAck(w io.Writer, ack supplyAck) error {
	var raw [83]byte
	copy(raw[:4], "GSA1")
	copy(raw[4:20], ack.Epoch[:])
	binary.LittleEndian.PutUint16(raw[20:22], ack.Shard)
	binary.LittleEndian.PutUint64(raw[22:30], ack.Sequence)
	copy(raw[30:62], ack.PayloadHash[:])
	binary.LittleEndian.PutUint16(raw[62:64], ack.New)
	binary.LittleEndian.PutUint16(raw[64:66], ack.Duplicate)
	binary.LittleEndian.PutUint64(raw[66:74], ack.QueueTx)
	binary.LittleEndian.PutUint64(raw[74:82], ack.QueueBytes)
	if ack.Final {
		raw[82] = 1
	}
	return writeSupplyBytes(w, raw[:])
}

func readSupplyAck(r io.Reader, epoch [16]byte) (supplyAck, error) {
	var raw [83]byte
	var ack supplyAck
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return ack, err
	}
	copy(ack.Epoch[:], raw[4:20])
	if string(raw[:4]) != "GSA1" || ack.Epoch != epoch || raw[82] > 1 {
		return supplyAck{}, errors.New("invalid supply ACK/session epoch")
	}
	ack.Shard = binary.LittleEndian.Uint16(raw[20:22])
	ack.Sequence = binary.LittleEndian.Uint64(raw[22:30])
	copy(ack.PayloadHash[:], raw[30:62])
	ack.New = binary.LittleEndian.Uint16(raw[62:64])
	ack.Duplicate = binary.LittleEndian.Uint16(raw[64:66])
	ack.QueueTx = binary.LittleEndian.Uint64(raw[66:74])
	ack.QueueBytes = binary.LittleEndian.Uint64(raw[74:82])
	ack.Final = raw[82] == 1
	return ack, nil
}

func writeSupplyBytes(w io.Writer, raw []byte) error {
	for len(raw) != 0 {
		n, err := w.Write(raw)
		if n < 0 || n > len(raw) {
			return errors.New("invalid supply write count")
		}
		raw = raw[n:]
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
