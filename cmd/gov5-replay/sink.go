package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type supplySink struct {
	manifest                      supplyManifest
	epoch                         [16]byte
	credits                       *supplyCredits
	unique, wireBytes, duplicates atomic.Uint64
	mu                            sync.Mutex
	connected                     []bool
	completed                     []bool
	failure                       error
	onFailure                     func(error)
}

func newSupplySink(manifest supplyManifest) (*supplySink, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	sink := &supplySink{manifest: manifest, credits: newSupplyCredits(2_000_000, 512<<20), connected: make([]bool, manifest.Descriptor.ShardCount), completed: make([]bool, manifest.Descriptor.ShardCount)}
	if _, err := rand.Read(sink.epoch[:]); err != nil {
		return nil, err
	}
	return sink, nil
}

func (s *supplySink) fail(err error) {
	s.mu.Lock()
	first := s.failure == nil
	if s.failure == nil {
		s.failure = err
	}
	s.mu.Unlock()
	if first && s.onFailure != nil {
		s.onFailure(err)
	}
}

func (s *supplySink) integrity() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure != nil {
		return s.failure
	}
	for _, complete := range s.completed {
		if !complete {
			return errors.New("corpus shard integrity not yet complete")
		}
	}
	if s.unique.Load() != s.manifest.Descriptor.TotalTransactions() {
		return errors.New("receiver total differs from finalized manifest")
	}
	return nil
}

// Every accepted shard has a bounded reorder/deduplication window. An old
// sequence outside that window is rejected rather than counted twice. This is
// in-memory session idempotency, not a durable resume protocol.
func (s *supplySink) consume(ctx context.Context, stream io.ReadWriter) error {
	shard, err := readSupplyHello(stream, s.manifest)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if s.connected[shard] {
		s.mu.Unlock()
		return errors.New("shard reconnect forbidden after unknown transport outcome")
	}
	s.connected[shard] = true
	s.mu.Unlock()
	if err := writeSupplyWelcome(stream, s.manifest.Digest, s.epoch); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(stream, 128<<10)
	hasher := sha256.New()
	meta := s.manifest.Shards[shard]
	var next, transactions, encodedBytes uint64
	var recent [supplyWindow]struct {
		sequence uint64
		digest   types.Hash
		count    uint16
	}
	var scratch []byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Reserve globally before ReadCorpusFrame allocates/reads the payload.
		// Its own independent parser still verifies all hostile lengths.
		header, err := reader.Peek(sharedexec.CorpusWireHeaderSize)
		if err == io.EOF && len(header) == 0 {
			break
		}
		if err != nil {
			return err
		}
		count := binary.LittleEndian.Uint16(header[46:48])
		length := binary.LittleEndian.Uint32(header[48:52])
		if count == 0 || count > sharedexec.CorpusMaxFrameTxs || length > sharedexec.CorpusMaxFramePayload || uint64(length) < uint64(count)*5 {
			return errors.New("receiver frame length/count exceeds budget")
		}
		reserved := uint64(length) + sharedexec.CorpusWireHeaderSize
		if err := s.credits.reserve(ctx, uint64(count), reserved); err != nil {
			return err
		}
		frame, reusable, readErr := sharedexec.ReadCorpusFrame(reader, scratch)
		scratch = reusable
		if readErr == nil {
			readErr = frame.Validate(s.manifest.Descriptor)
		}
		if readErr == nil && frame.Shard != shard {
			readErr = errors.New("frame moved between shard connections")
		}
		if readErr != nil {
			s.credits.release(uint64(count), reserved)
			return readErr
		}
		ack := supplyAck{Epoch: s.epoch, Shard: shard, Sequence: frame.Sequence, PayloadHash: frame.PayloadHash}
		switch {
		case frame.Sequence == next:
			if next >= meta.Frames {
				s.credits.release(uint64(count), reserved)
				return errors.New("frame exceeds finalized shard length")
			}
			header, _ := frame.Header()
			_, _ = hasher.Write(header[:])
			_, _ = hasher.Write(frame.Payload)
			recent[next%supplyWindow].sequence = next
			recent[next%supplyWindow].digest = frame.PayloadHash
			recent[next%supplyWindow].count = count
			next++
			transactions += uint64(count)
			encodedBytes += reserved
			s.unique.Add(uint64(count))
			s.wireBytes.Add(reserved)
			ack.New = count
		case frame.Sequence < next && next-frame.Sequence <= supplyWindow:
			prior := recent[frame.Sequence%supplyWindow]
			if prior.sequence != frame.Sequence || prior.digest != frame.PayloadHash || prior.count != count {
				s.credits.release(uint64(count), reserved)
				return errors.New("same frame ID changed payload")
			}
			s.duplicates.Add(uint64(count))
			ack.Duplicate = count
		default:
			s.credits.release(uint64(count), reserved)
			return errors.New("out-of-order or expired duplicate frame")
		}
		// The sink has consumed the bytes; no queue retains transaction bodies.
		// Client credits remain charged until this exact ACK is received.
		s.credits.release(uint64(count), reserved)
		ack.QueueTx, ack.QueueBytes = s.credits.used()
		if err := writeSupplyAck(stream, ack); err != nil {
			return err
		}
	}
	var digest types.Hash
	copy(digest[:], hasher.Sum(nil))
	if next != meta.Frames || transactions != meta.Transactions || encodedBytes != meta.Bytes || digest != meta.SHA256 {
		return fmt.Errorf("shard %d finalized manifest integrity mismatch", shard)
	}
	s.mu.Lock()
	s.completed[shard] = true
	s.mu.Unlock()
	return writeSupplyAck(stream, supplyAck{Epoch: s.epoch, Shard: shard, Sequence: next, PayloadHash: digest, Final: true})
}

// listenSupply runs a single bounded session. Every connection must supply the
// pinned manifest; cancellation closes live sockets, unblocking reads/writes.
func (s *supplySink) listenSupply(ctx context.Context, listener net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var group sync.WaitGroup
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = listener.Close()
		case <-done:
		}
	}()
	var acceptErr error
	for count := 0; count < int(s.manifest.Descriptor.ShardCount); count++ {
		conn, err := listener.Accept()
		if err != nil {
			acceptErr = err
			cancel()
			break
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
			finished := make(chan struct{})
			defer close(finished)
			go func() {
				select {
				case <-ctx.Done():
					_ = conn.Close()
				case <-finished:
				}
			}()
			if err := s.consume(ctx, conn); err != nil {
				s.fail(err)
				cancel()
			}
		}()
	}
	group.Wait()
	s.mu.Lock()
	failure := s.failure
	s.mu.Unlock()
	if failure != nil {
		return failure
	}
	if acceptErr != nil {
		return acceptErr
	}
	return s.integrity()
}
