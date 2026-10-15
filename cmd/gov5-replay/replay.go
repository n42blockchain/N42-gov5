package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type supplyPacer struct {
	start   time.Time
	rate    uint64
	tickets atomic.Uint64
}

func (p *supplyPacer) wait(ctx context.Context, count uint16) error {
	ticket := p.tickets.Add(uint64(count))
	target := p.start.Add(time.Duration(ticket * uint64(time.Second) / p.rate))
	delay := time.Until(target)
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type supplyPending struct {
	sequence uint64
	hash     types.Hash
	count    uint16
	bytes    uint64
}

type replayCounters struct {
	read, written, acknowledged, encodedBytes atomic.Uint64
	epochMu                                   sync.Mutex
	epoch                                     [16]byte
}

func (c *replayCounters) bindEpoch(epoch [16]byte) error {
	c.epochMu.Lock()
	defer c.epochMu.Unlock()
	if c.epoch == ([16]byte{}) {
		c.epoch = epoch
	}
	if c.epoch != epoch {
		return errors.New("receiver epoch changed across shard connections; stop without resume")
	}
	return nil
}

func replaySupplyShard(ctx context.Context, root, socket string, manifest supplyManifest, shard uint16, credits *supplyCredits, pacer *supplyPacer, counters *replayCounters, ready chan<- error, start <-chan struct{}, onFailure func(error)) (returnErr error) {
	var conn net.Conn
	defer func() {
		// Report the client's original error before closing its socket;
		// otherwise the sink's resulting EOF can win first-error recording.
		if returnErr != nil && onFailure != nil {
			onFailure(returnErr)
		}
		if conn != nil {
			_ = conn.Close()
		}
	}()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		ready <- err
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	closed := make(chan struct{})
	defer close(closed)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-closed:
		}
	}()
	if err := writeSupplyHello(conn, manifest.Digest, shard); err != nil {
		ready <- err
		return err
	}
	epoch, err := readSupplyWelcome(conn, manifest.Digest)
	if err != nil {
		ready <- err
		return err
	}
	if err := counters.bindEpoch(epoch); err != nil {
		ready <- err
		return err
	}
	meta := manifest.Shards[shard]
	file, err := openSupplyFile(filepath.Join(root, meta.File), int64(meta.Bytes))
	if err != nil {
		ready <- err
		return err
	}
	defer file.Close()
	reader := bufio.NewReaderSize(file, 8<<20)
	ready <- nil
	select {
	case <-start:
	case <-ctx.Done():
		return ctx.Err()
	}
	pending := make([]supplyPending, 0, supplyWindow)
	defer func() {
		// Unknown outcomes stop the session, but all local reservations are
		// still released. No reconnect or replay is attempted.
		for _, frame := range pending {
			credits.release(uint64(frame.count), frame.bytes)
		}
	}()
	acknowledge := func() error {
		ack, err := readSupplyAck(conn, epoch)
		if err != nil {
			return err
		}
		first := pending[0]
		if ack.Final || ack.Shard != shard || ack.Sequence != first.sequence || ack.PayloadHash != first.hash || ack.New != first.count || ack.Duplicate != 0 {
			return errors.New("unknown/mismatched supply ACK: stop without retransmission")
		}
		credits.release(uint64(first.count), first.bytes)
		pending = pending[1:]
		counters.acknowledged.Add(uint64(first.count))
		return nil
	}
	var scratch []byte
	for sequence := uint64(0); sequence < meta.Frames; sequence++ {
		if len(pending) == supplyWindow {
			if err := acknowledge(); err != nil {
				return err
			}
		}
		frame, reusable, err := sharedexec.ReadCorpusFrame(reader, scratch)
		scratch = reusable
		if err != nil {
			return err
		}
		if err := frame.Validate(manifest.Descriptor); err != nil {
			return err
		}
		if frame.Shard != shard || frame.Sequence != sequence {
			return errors.New("local corpus shard sequence mismatch")
		}
		counters.read.Add(uint64(frame.Transactions))
		encoded := uint64(sharedexec.CorpusWireHeaderSize + len(frame.Payload))
		for {
			reserved, err := credits.tryReserve(uint64(frame.Transactions), encoded)
			if err != nil {
				return err
			}
			if reserved {
				break
			}
			// Drain our own acknowledgements while global credits are full.
			// Waiting for a complete 32-frame window here can deadlock all
			// shards when byte credits bind before the per-shard limit.
			if len(pending) != 0 {
				if err := acknowledge(); err != nil {
					return err
				}
			} else {
				if err := credits.reserve(ctx, uint64(frame.Transactions), encoded); err != nil {
					return err
				}
				break
			}
		}
		pending = append(pending, supplyPending{sequence: sequence, hash: frame.PayloadHash, count: frame.Transactions, bytes: encoded})
		if err := pacer.wait(ctx, frame.Transactions); err != nil {
			return err
		}
		if err := sharedexec.WriteCorpusFrame(conn, frame); err != nil {
			return err
		}
		counters.written.Add(uint64(frame.Transactions))
		counters.encodedBytes.Add(encoded)
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		return errors.New("local shard has unmanifested trailing bytes")
	}
	for len(pending) != 0 {
		if err := acknowledge(); err != nil {
			return err
		}
	}
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("explicit Unix supply connection required")
	}
	if err := unix.CloseWrite(); err != nil {
		return err
	}
	final, err := readSupplyAck(conn, epoch)
	if err != nil {
		return err
	}
	if !final.Final || final.Shard != shard || final.Sequence != meta.Frames || final.PayloadHash != meta.SHA256 || final.New != 0 || final.Duplicate != 0 {
		return errors.New("receiver did not finalize pinned shard integrity")
	}
	return nil
}

// openSupplyFile rejects symlinks and size changes before a bounded reader is
// created. The receiver's final SHA verifies content, including modifications
// after this check, before any qualification can pass.
func openSupplyFile(path string, size int64) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || (size >= 0 && info.Size() != size) {
		return nil, errors.New("regular private corpus file of exact size required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		_ = file.Close()
		return nil, errors.New("corpus file changed while opening")
	}
	return file, nil
}

func loadSupplyManifest(root string) (supplyManifest, error) {
	read := func(name string, limit int64) ([]byte, error) {
		file, err := openSupplyFile(filepath.Join(root, name), -1)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(raw)) > limit {
			return nil, errors.New("supply metadata exceeds size limit")
		}
		return raw, nil
	}
	raw, err := read("manifest.json", sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return supplyManifest{}, err
	}
	manifest, err := parseSupplyManifest(raw)
	if err != nil {
		return supplyManifest{}, err
	}
	genesisRaw, err := read("genesis.json", 16<<20)
	if err != nil {
		return supplyManifest{}, err
	}
	if types.Hash(sha256.Sum256(genesisRaw)) != manifest.Descriptor.GenesisFileHash {
		return supplyManifest{}, errors.New("actual supply genesis file differs from descriptor")
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(genesisRaw, &genesis); err != nil {
		return supplyManifest{}, err
	}
	if err := validateSupplyGenesis(&genesis); err != nil {
		return supplyManifest{}, err
	}
	b, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).ToBlock()
	if err != nil {
		return supplyManifest{}, err
	}
	if b.Hash() != manifest.Descriptor.Identity.GenesisHash {
		return supplyManifest{}, errors.New("actual genesis header differs from corpus identity")
	}
	return manifest, nil
}

type supplySample struct {
	Elapsed float64 `json:"elapsed_seconds"`
	Unique  uint64  `json:"receiver_unique_provisional"`
	Bytes   uint64  `json:"receiver_encoded_bytes_provisional"`
}

// The interval conservatively surrounds its counter boundaries: start time
// precedes the start counters, and end counters precede the end timestamp.
func supplyBoundary(start time.Time, sink *supplySink, begin bool) supplySample {
	var result supplySample
	if begin {
		result.Elapsed = time.Since(start).Seconds()
	}
	result.Unique = sink.unique.Load()
	result.Bytes = sink.wireBytes.Load()
	if !begin {
		result.Elapsed = time.Since(start).Seconds()
	}
	return result
}

func qualifySupply(ctx context.Context, root, attempt string, offered uint64, warm, measured time.Duration) error {
	if offered < 6_000_000 || offered > 100_000_000 || warm < 0 || measured < 20*time.Second || measured > 30*time.Second {
		return errors.New("qualification requires offered >=6M/s and measured 20..30 seconds")
	}
	if filepath.Base(attempt) != attempt || len(attempt) == 0 || len(attempt) > 48 || attempt == "." || attempt == ".." {
		return errors.New("new attempt basename required")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root || !filepath.IsAbs(root) || !strings.HasPrefix(root, supplyRoot+string(filepath.Separator)) {
		return errors.New("absolute unaliased corpus root required")
	}
	manifest, err := loadSupplyManifest(root)
	if err != nil {
		return err
	}
	if manifest.Descriptor.TotalTransactions() <= offered*uint64((warm+measured)/time.Second+1) {
		return errors.New("unique corpus insufficient for warmup + measured window + margin")
	}
	output := filepath.Join(root, "qualification-"+attempt)
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	socket := filepath.Join(output, "s.sock")
	if len(socket) >= 104 {
		return errors.New("dedicated Unix socket path too long; use a shorter fresh attempt name")
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var firstError error
	var errorMu sync.Mutex
	recordError := func(err error) {
		if err == nil {
			return
		}
		errorMu.Lock()
		if firstError == nil {
			firstError = err
		}
		errorMu.Unlock()
		cancel()
	}
	sink, err := newSupplySink(manifest)
	if err != nil {
		return err
	}
	sink.onFailure = recordError
	sinkDone := make(chan error, 1)
	go func() { sinkDone <- sink.listenSupply(ctx, listener) }()
	credits := newSupplyCredits(2_000_000, 512<<20)
	var counters replayCounters
	pacer := &supplyPacer{rate: offered}
	ready := make(chan error, manifest.Descriptor.ShardCount)
	start := make(chan struct{})
	errorsByShard := make([]error, manifest.Descriptor.ShardCount)
	var group sync.WaitGroup
	for shard := uint16(0); shard < manifest.Descriptor.ShardCount; shard++ {
		group.Add(1)
		go func(shard uint16) {
			defer group.Done()
			errorsByShard[shard] = replaySupplyShard(ctx, root, socket, manifest, shard, credits, pacer, &counters, ready, start, recordError)
			recordError(errorsByShard[shard])
		}(shard)
	}
	var runErr error
	for range manifest.Shards {
		select {
		case err := <-ready:
			if err != nil && runErr == nil {
				runErr = err
			}
		case <-ctx.Done():
			if runErr == nil {
				runErr = ctx.Err()
			}
		}
	}
	pacer.start = time.Now()
	close(start)
	var samples []supplySample
	sample := func() supplySample {
		return supplySample{Elapsed: time.Since(pacer.start).Seconds(), Unique: sink.unique.Load(), Bytes: sink.wireBytes.Load()}
	}
	wait := func(duration time.Duration) error {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				samples = append(samples, sample())
			}
		}
	}
	if runErr == nil {
		runErr = wait(warm)
	}
	begin := supplyBoundary(pacer.start, sink, true)
	if runErr == nil {
		runErr = wait(measured)
	}
	end := supplyBoundary(pacer.start, sink, false)
	group.Wait()
	sinkErr := <-sinkDone
	if runErr == nil {
		runErr = sinkErr
	}
	for _, err := range errorsByShard {
		if err != nil && runErr == nil {
			runErr = err
		}
	}
	errorMu.Lock()
	if firstError != nil {
		runErr = firstError
	}
	errorMu.Unlock()
	elapsed := end.Elapsed - begin.Elapsed
	var rate float64
	if elapsed > 0 {
		rate = float64(end.Unique-begin.Unique) / elapsed
	}
	passed := runErr == nil && elapsed >= measured.Seconds() && rate >= 6_000_000 && end.Unique < manifest.Descriptor.TotalTransactions() && sink.integrity() == nil
	result := map[string]any{"scope": "raw-signed-wire-transport-only", "qualified": passed, "manifest_sha256": manifest.Digest, "corpus_id": manifest.CorpusID, "session_epoch": fmt.Sprintf("%x", sink.epoch), "offered_tps": offered, "warmup_seconds": warm.Seconds(), "window_start": begin, "window_end": end, "actual_window_seconds": elapsed, "receiver_unique_wire_tps": rate, "receiver_encoded_gib_per_second": float64(end.Bytes-begin.Bytes) / elapsed / (1 << 30), "all_shards_integrity_verified": sink.integrity() == nil, "corpus_exhausted_in_window": end.Unique >= manifest.Descriptor.TotalTransactions(), "receiver_unique_final": sink.unique.Load(), "duplicates": sink.duplicates.Load(), "sender_read": counters.read.Load(), "sender_written": counters.written.Load(), "sender_acknowledged": counters.acknowledged.Load(), "crypto_verified": 0, "on_chain_committed": 0, "samples": samples, "corpus_cache_state": "not explicitly preloaded; kernel page cache uncontrolled", "gomaxprocs": runtime.GOMAXPROCS(0), "sender_file_buffer_bytes_per_shard": 8 << 20, "global_unacknowledged_tx_limit": 2_000_000, "global_unacknowledged_encoded_byte_limit": 512 << 20}
	if runErr != nil {
		result["error"] = runErr.Error()
	}
	if err := exclusiveJSON(filepath.Join(output, "result.json"), result); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	if !passed {
		return errors.New("raw transport qualification did not meet the actual >=6M/s gate")
	}
	return nil
}
