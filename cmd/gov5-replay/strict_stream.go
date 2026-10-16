package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type strictSource struct {
	file *os.File
	info os.FileInfo
}

func closeStrictSources(files []strictSource) {
	for _, file := range files {
		if file.file != nil {
			_ = file.file.Close()
		}
	}
}

// A timed prefix cannot use the raw sink's EOF proof. Hash every complete shard
// before dispatch, seek the same open descriptors, and retain those descriptors.
func openStrictSources(ctx context.Context, root string, m supplyManifest) (files []strictSource, err error) {
	defer func() {
		if err != nil {
			closeStrictSources(files)
		}
	}()
	buffer := make([]byte, 8<<20)
	for _, shard := range m.Shards {
		path := filepath.Join(root, shard.File)
		before, e := os.Lstat(path)
		if e != nil {
			return files, e
		}
		owner, ok := before.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Geteuid()) || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() != int64(shard.Bytes) {
			return files, errors.New("strict source must be an owned private regular file of exact size")
		}
		file, e := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			return files, e
		}
		files = append(files, strictSource{file: file, info: before})
		after, e := file.Stat()
		if e != nil {
			return files, e
		}
		if !os.SameFile(before, after) {
			return files, errors.New("strict source changed while opening")
		}
		hash := sha256.New()
		var n int64
		for {
			if e := ctx.Err(); e != nil {
				return files, e
			}
			count, readErr := file.Read(buffer)
			if count > 0 {
				hash.Write(buffer[:count])
				n += int64(count)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return files, readErr
			}
			if count == 0 {
				return files, io.ErrNoProgress
			}
		}
		if n != int64(shard.Bytes) || types.BytesToHash(hash.Sum(nil)) != shard.SHA256 {
			return files, errors.New("strict full source shard integrity failed")
		}
		if _, e := file.Seek(0, io.SeekStart); e != nil {
			return files, e
		}
		if e := checkStrictSource(files[len(files)-1]); e != nil {
			return files, e
		}
	}
	return files, nil
}
func checkStrictSource(source strictSource) error {
	after, err := source.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(source.info, after) || source.info.Size() != after.Size() || !source.info.ModTime().Equal(after.ModTime()) || after.Mode().Perm() != 0600 {
		return errors.New("strict source file changed after integrity check")
	}
	return nil
}

type strictAdmitter interface {
	Admit(context.Context, sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error)
	Epoch() [16]byte
	Close() error
}
type strictStreamCounters struct {
	read, encodedBytes, admitted, busy, snapshot, readNanos, paceNanos, admitNanos, busyWaitNanos, snapshotWaitNanos atomic.Uint64
	exhaustedShards                                                                                                  atomic.Uint64
	firstExhaustedNanos                                                                                              atomic.Int64
}
type strictStreamSample struct {
	Read                  uint64  `json:"source_read_transactions"`
	EncodedBytes          uint64  `json:"source_read_encoded_bytes"`
	Admitted              uint64  `json:"acknowledged_unique_admitted"`
	BusyRetries           uint64  `json:"busy_retries"`
	SnapshotRetries       uint64  `json:"snapshot_retries"`
	ReadSeconds           float64 `json:"source_read_seconds"`
	PaceSeconds           float64 `json:"ready_wait_pacer_seconds"`
	AdmitSeconds          float64 `json:"admit_roundtrip_seconds"`
	BusyWaitSeconds       float64 `json:"ready_wait_credit_seconds"`
	SnapshotWaitSeconds   float64 `json:"snapshot_retry_wait_seconds"`
	ExhaustedShards       uint64  `json:"exhausted_shards"`
	FirstExhaustedSeconds float64 `json:"first_observed_shard_exhaustion_seconds"`
}

func (c *strictStreamCounters) sample() strictStreamSample {
	return strictStreamSample{
		Read: c.read.Load(), EncodedBytes: c.encodedBytes.Load(), Admitted: c.admitted.Load(),
		BusyRetries: c.busy.Load(), SnapshotRetries: c.snapshot.Load(),
		ReadSeconds: float64(c.readNanos.Load()) / 1e9, PaceSeconds: float64(c.paceNanos.Load()) / 1e9,
		AdmitSeconds: float64(c.admitNanos.Load()) / 1e9, BusyWaitSeconds: float64(c.busyWaitNanos.Load()) / 1e9,
		SnapshotWaitSeconds: float64(c.snapshotWaitNanos.Load()) / 1e9,
		ExhaustedShards:     c.exhaustedShards.Load(), FirstExhaustedSeconds: float64(c.firstExhaustedNanos.Load()) / 1e9,
	}
}

// Stop dispatch between requests. A granted frame keeps its work context until
// its final ACK; ending a measurement must not manufacture an unknown outcome.
func strictAdmitFrame(ctx, dispatch context.Context, c strictAdmitter, frame sharedexec.CorpusFrame, epoch [16]byte, counters *strictStreamCounters) (accepted bool, err error) {
	if c.Epoch() != epoch {
		return false, errors.New("strict welcome epoch differs before first frame")
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if dispatch.Err() != nil {
			return false, nil
		}
		// Backpressure consumes the measured dispatch window, not the next
		// request's IO allowance. Reusing one context across known-zero replies
		// could close a newly granted request just as its final ACK arrived.
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		start := time.Now()
		ack, err := c.Admit(work, frame)
		cancel() // Admit has attempted to stop its callback before returning.
		counters.admitNanos.Add(uint64(time.Since(start)))
		if err == nil {
			if ack.Status.Epoch != epoch || ack.NewlyAdmitted != int(frame.Transactions) || ack.Duplicates != 0 {
				return false, errors.New("strict final ACK not pinned unique admission")
			}
			counters.admitted.Add(uint64(ack.NewlyAdmitted))
			return true, nil
		}
		if !errors.Is(err, sharedexec.ErrIngressCreditsExhausted) && !errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
			return false, err
		}
		if ack.NewlyAdmitted != 0 || ack.Duplicates != 0 {
			return false, fmt.Errorf("%w: retry carried admission", sharedexec.ErrIngressUnknownOutcome)
		}
		if errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
			counters.snapshot.Add(1)
		} else {
			counters.busy.Add(1)
		}
		start = time.Now()
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-dispatch.Done():
			timer.Stop()
			return false, nil
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
		elapsed := uint64(time.Since(start))
		if errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
			counters.snapshotWaitNanos.Add(elapsed)
		} else {
			counters.busyWaitNanos.Add(elapsed)
		}
	}
}

type strictShardProgress struct {
	Sequence     uint64 `json:"next_sequence"`
	Transactions uint64 `json:"admitted_transactions"`
	Exhausted    bool   `json:"source_exhausted"`
}

func strictPumpShard(ctx, dispatch context.Context, source strictSource, m supplyManifest, shard uint16, c strictAdmitter, epoch [16]byte, pacer *supplyPacer, counters *strictStreamCounters, ready chan<- error, start <-chan struct{}, progress *strictShardProgress) error {
	reader := bufio.NewReaderSize(source.file, 8<<20)
	var scratch []byte
	for seq := uint64(0); seq < m.Shards[shard].Frames; seq++ {
		if dispatch.Err() != nil {
			if seq == 0 {
				ready <- dispatch.Err()
			}
			return nil
		}
		before := time.Now()
		frame, next, err := sharedexec.ReadCorpusFrame(reader, scratch)
		counters.readNanos.Add(uint64(time.Since(before)))
		scratch = next
		if err == nil {
			err = frame.Validate(m.Descriptor)
		}
		if err == nil && (frame.CorpusID != m.CorpusID || frame.Shard != shard || frame.Sequence != seq) {
			err = errors.New("strict source sequence/identity mismatch")
		}
		if seq == 0 {
			ready <- err
			if err == nil {
				select {
				case <-start:
				case <-dispatch.Done():
					return nil
				}
			}
		}
		if err != nil {
			return err
		}
		counters.read.Add(uint64(frame.Transactions))
		counters.encodedBytes.Add(uint64(sharedexec.CorpusWireHeaderSize + len(frame.Payload)))
		before = time.Now()
		err = pacer.wait(dispatch, frame.Transactions)
		counters.paceNanos.Add(uint64(time.Since(before)))
		if err != nil {
			if dispatch.Err() != nil {
				return nil
			}
			return err
		}
		accepted, err := strictAdmitFrame(ctx, dispatch, c, frame, epoch, counters)
		if err != nil {
			return fmt.Errorf("strict shard %d sequence %d payload %s: %w", shard, seq, frame.PayloadHash.Hex(), err)
		}
		if !accepted {
			return nil
		}
		progress.Sequence = seq + 1
		progress.Transactions += uint64(frame.Transactions)
	}
	observedAt := time.Since(pacer.start).Nanoseconds()
	if _, err := reader.ReadByte(); err != io.EOF {
		return errors.New("strict source has trailing frame bytes")
	}
	progress.Exhausted = true
	counters.exhaustedShards.Add(1)
	for {
		old := counters.firstExhaustedNanos.Load()
		if old != 0 && old <= observedAt {
			break
		}
		if counters.firstExhaustedNanos.CompareAndSwap(old, observedAt) {
			break
		}
	}
	return checkStrictSource(source)
}
