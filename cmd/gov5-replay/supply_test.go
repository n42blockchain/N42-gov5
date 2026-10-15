package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/params"
)

func supplyFixture(t *testing.T) (string, supplyManifest) {
	t.Helper()
	root := t.TempDir()
	d := sharedexec.CorpusDescriptor{Version: 1, Identity: sharedexec.Identity{Version: 1, Namespace: params.PerformanceNamespace, ChainID: "4205001", GenesisHash: types.Hash{1}}, GenesisFileHash: types.Hash{2}, Codec: sharedexec.CorpusCodec, Signature: sharedexec.CorpusSignature, KeyDerivation: sharedexec.CorpusKeyDerivation, SeedCommitment: types.Hash{3}, SenderCount: 5, NoncesPerSender: 5, NonceStart: 7, RecipientRule: sharedexec.CorpusRecipientRule, RecipientCount: 3, ShardCount: 3, FrameTransactions: 2}
	id, err := d.ID()
	if err != nil {
		t.Fatal(err)
	}
	m := sharedexec.CorpusManifest{Descriptor: d, CorpusID: id}
	seen := make(map[[2]uint64]bool)
	for shard := uint16(0); shard < d.ShardCount; shard++ {
		var stream bytes.Buffer
		meta := sharedexec.CorpusShard{Index: shard, File: []string{"shard-00.gcf", "shard-01.gcf", "shard-02.gcf"}[shard]}
		for seq := uint64(0); ; seq++ {
			sender, nonce, count, err := d.FrameRange(shard, seq)
			if err != nil {
				break
			}
			txs := make([][]byte, count)
			for i := range txs {
				pair := [2]uint64{uint64(sender), nonce + uint64(i)}
				if seen[pair] {
					t.Fatal("repeated sender/nonce in uneven fixture")
				}
				seen[pair] = true
				txs[i] = make([]byte, 16)
				binary.LittleEndian.PutUint64(txs[i][:8], pair[0])
				binary.LittleEndian.PutUint64(txs[i][8:], pair[1])
			}
			frame, err := sharedexec.MakeCorpusFrame(sharedexec.CorpusFrameID{CorpusID: id, Shard: shard, Sequence: seq}, txs)
			if err != nil {
				t.Fatal(err)
			}
			if err := sharedexec.WriteCorpusFrame(&stream, frame); err != nil {
				t.Fatal(err)
			}
			meta.Transactions += uint64(count)
			meta.Frames++
		}
		meta.Bytes = uint64(stream.Len())
		meta.SHA256 = types.Hash(sha256.Sum256(stream.Bytes()))
		if err := os.WriteFile(filepath.Join(root, meta.File), stream.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		m.Shards = append(m.Shards, meta)
	}
	if uint64(len(seen)) != d.TotalTransactions() {
		t.Fatal("uneven shard tail omitted nonce range")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseSupplyManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	return root, manifest
}

func TestSupplyActualUnixReplayVerifiesEveryShard(t *testing.T) {
	root, manifest := supplyFixture(t)
	sink, err := newSupplySink(manifest)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sink.listenSupply(ctx, listener) }()
	credits := newSupplyCredits(10, 512)
	pacer := &supplyPacer{start: time.Now(), rate: 100_000_000}
	var counters replayCounters
	ready := make(chan error, 3)
	start := make(chan struct{})
	close(start)
	errs := make(chan error, 3)
	for shard := uint16(0); shard < 3; shard++ {
		go func(shard uint16) {
			errs <- replaySupplyShard(ctx, root, socket, manifest, shard, credits, pacer, &counters, ready, start, nil)
		}(shard)
	}
	for range manifest.Shards {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := sink.integrity(); err != nil {
		t.Fatal(err)
	}
	if sink.unique.Load() != 25 || counters.acknowledged.Load() != 25 || counters.read.Load() != 25 || counters.written.Load() != 25 {
		t.Fatal("receiver/sender counts differ")
	}
	if tx, bytes := credits.used(); tx != 0 || bytes != 0 {
		t.Fatal("unacknowledged credit leak")
	}
}

func TestSupplyBadHelloCancelsMissingShardAccept(t *testing.T) {
	_, manifest := supplyFixture(t)
	sink, err := newSupplySink(manifest)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// No context deadline: the first protocol error itself must close Accept,
	// even though two required shards will never connect.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sink.listenSupply(ctx, listener) }()
	conn, err := net.Dial("unix", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	wrong := manifest.Digest
	wrong[0] ^= 1
	if err := writeSupplyHello(conn, wrong, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "handshake mismatch") {
			t.Fatalf("lost original protocol failure: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("bad hello left Accept blocked")
	}
}

func TestSupplyUnknownAckStopsWithoutReplayingOrRetainingCredits(t *testing.T) {
	root, manifest := supplyFixture(t)
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	failureRecorded := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		shard, err := readSupplyHello(conn, manifest)
		if err != nil {
			serverDone <- err
			return
		}
		epoch := [16]byte{1}
		if err := writeSupplyWelcome(conn, manifest.Digest, epoch); err != nil {
			serverDone <- err
			return
		}
		for seq := uint64(0); seq < manifest.Shards[shard].Frames; seq++ {
			frame, _, err := sharedexec.ReadCorpusFrame(conn, nil)
			if err != nil {
				serverDone <- err
				return
			}
			if frame.Sequence != seq {
				serverDone <- errors.New("sender replayed prior frame")
				return
			}
			if seq == 0 {
				// A transport success carrying the wrong acknowledgement is
				// an unknown outcome, not permission to resend the corpus.
				if err := writeSupplyAck(conn, supplyAck{Epoch: epoch, Shard: shard, Sequence: 999, PayloadHash: frame.PayloadHash, New: frame.Transactions}); err != nil {
					serverDone <- err
					return
				}
			}
		}
		var extra [1]byte
		_, err = conn.Read(extra[:])
		if err != io.EOF {
			serverDone <- errors.New("sender retransmitted after unknown ACK")
			return
		}
		select {
		case err := <-failureRecorded:
			if !strings.Contains(err.Error(), "mismatched supply ACK") {
				serverDone <- err
				return
			}
		default:
			serverDone <- errors.New("client closed socket before recording its original failure")
			return
		}
		serverDone <- nil
	}()
	credits := newSupplyCredits(100, 1<<20)
	var counters replayCounters
	ready := make(chan error, 1)
	start := make(chan struct{})
	close(start)
	err = replaySupplyShard(ctx, root, listener.Addr().String(), manifest, 0, credits, &supplyPacer{start: time.Now(), rate: 100_000_000}, &counters, ready, start, func(err error) { failureRecorded <- err })
	if err == nil || !strings.Contains(err.Error(), "mismatched supply ACK") {
		t.Fatalf("unknown ACK did not stop sender: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if counters.acknowledged.Load() != 0 {
		t.Fatal("unknown ACK counted as accepted")
	}
	if tx, bytes := credits.used(); tx != 0 || bytes != 0 {
		t.Fatal("unknown ACK leaked local credits")
	}
}

func TestSupplyCreditsBoundBothBudgetsAndCancel(t *testing.T) {
	credits := newSupplyCredits(3, 10)
	if err := credits.reserve(context.Background(), 2, 5); err != nil {
		t.Fatal(err)
	}
	if ok, _ := credits.tryReserve(2, 1); ok {
		t.Fatal("reused global tx credit")
	}
	if ok, _ := credits.tryReserve(1, 6); ok {
		t.Fatal("reused global byte credit")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := credits.reserve(ctx, 2, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	credits.release(2, 5)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 10 {
				if err := credits.reserve(context.Background(), 1, 4); err == nil {
					credits.release(1, 4)
				}
			}
		}()
	}
	group.Wait()
	if tx, bytes := credits.used(); tx != 0 || bytes != 0 {
		t.Fatal("credits retained after consumption")
	}
}

func TestSupplyManifestPinsPayloadAndEpoch(t *testing.T) {
	root, manifest := supplyFixture(t)
	changed := manifest.CorpusManifest
	changed.Shards = append([]sharedexec.CorpusShard(nil), changed.Shards...)
	changed.Shards[0].SHA256[0] ^= 1
	raw, _ := json.Marshal(changed)
	next, err := parseSupplyManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if next.CorpusID != manifest.CorpusID || next.Digest == manifest.Digest {
		t.Fatal("final manifest payload binding absent")
	}
	var hello bytes.Buffer
	if err := writeSupplyHello(&hello, manifest.Digest, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupplyHello(&hello, next); err == nil {
		t.Fatal("same descriptor bypassed final manifest pin")
	}
	var welcome bytes.Buffer
	if err := writeSupplyWelcome(&welcome, manifest.Digest, [16]byte{1}); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupplyWelcome(&welcome, next.Digest); err == nil {
		t.Fatal("welcome changed manifest")
	}
	var ack bytes.Buffer
	if err := writeSupplyAck(&ack, supplyAck{Epoch: [16]byte{1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := readSupplyAck(&ack, [16]byte{2}); err == nil {
		t.Fatal("receiver restart reused prior session ACK")
	}
	var counters replayCounters
	if err := counters.bindEpoch([16]byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := counters.bindEpoch([16]byte{2}); err == nil {
		t.Fatal("different shard receiver epochs accepted")
	}
	if _, err := openSupplyFile(filepath.Join(root, "shard-00.gcf"), 1); err == nil {
		t.Fatal("changed file size accepted")
	}
	if err := os.Symlink(filepath.Join(root, "shard-00.gcf"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := openSupplyFile(filepath.Join(root, "alias"), -1); err == nil {
		t.Fatal("symlink corpus accepted")
	}
}

func TestSupplyInvalidPreparationShapeDoesNotCreateDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "absent")
	for _, shape := range []struct {
		senders uint32
		nonces  uint64
		shards  uint16
	}{{0, 1, 1}, {12501, 1, 1}, {1, 0, 1}, {1, 1, 2}, {12500, 600_000_000, 64}} {
		if err := prepareCorpus(context.Background(), root, "unused", base, shape.senders, shape.nonces, shape.shards); err == nil {
			t.Fatal("invalid shape accepted")
		}
		if _, err := os.Lstat(root); !os.IsNotExist(err) {
			t.Fatal("invalid numeric shape generated files/keys")
		}
	}
}

func TestSupplyWriteRejectsZeroProgress(t *testing.T) {
	if err := writeSupplyBytes(zeroSupplyWriter{}, []byte{1}); err != io.ErrShortWrite {
		t.Fatal(err)
	}
}

func TestSupplySinkDeduplicatesAndRequiresFinalFileHash(t *testing.T) {
	root, manifest := supplyFixture(t)
	encoded, err := os.ReadFile(filepath.Join(root, manifest.Shards[0].File))
	if err != nil {
		t.Fatal(err)
	}
	var frames []sharedexec.CorpusFrame
	reader := bytes.NewReader(encoded)
	for reader.Len() != 0 {
		frame, _, err := sharedexec.ReadCorpusFrame(reader, nil)
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	for _, mode := range []string{"duplicate", "conflicting-duplicate", "swapped-payload"} {
		t.Run(mode, func(t *testing.T) {
			sink, err := newSupplySink(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var input, output bytes.Buffer
			if err := writeSupplyHello(&input, manifest.Digest, 0); err != nil {
				t.Fatal(err)
			}
			for i, original := range frames {
				frame := original
				if mode == "swapped-payload" && i < 2 {
					frame.Payload = frames[1-i].Payload
					frame.PayloadHash = frames[1-i].PayloadHash
					// Local descriptor/self-digest checks cannot prove this
					// is the originally offline-verified shard payload.
					if err := frame.Validate(manifest.Descriptor); err != nil {
						t.Fatal(err)
					}
				}
				if err := sharedexec.WriteCorpusFrame(&input, frame); err != nil {
					t.Fatal(err)
				}
				if i == 0 && mode != "swapped-payload" {
					if mode == "conflicting-duplicate" {
						frame.Payload = bytes.Clone(frame.Payload)
						frame.Payload[len(frame.Payload)-1] ^= 1
						frame.PayloadHash = types.Hash(sha256.Sum256(frame.Payload))
					}
					if err := sharedexec.WriteCorpusFrame(&input, frame); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = sink.consume(context.Background(), struct {
				io.Reader
				io.Writer
			}{Reader: &input, Writer: &output})
			switch mode {
			case "duplicate":
				if err != nil || sink.unique.Load() != manifest.Shards[0].Transactions || sink.duplicates.Load() != uint64(frames[0].Transactions) {
					t.Fatalf("duplicate counted as unique: %v", err)
				}
			case "conflicting-duplicate":
				if err == nil || !strings.Contains(err.Error(), "changed payload") {
					t.Fatalf("same ID changed payload accepted: %v", err)
				}
			case "swapped-payload":
				if err == nil || !strings.Contains(err.Error(), "integrity mismatch") {
					t.Fatalf("self-consistent frame swap bypassed final manifest: %v", err)
				}
			}
		})
	}
}

type zeroSupplyWriter struct{}

func (zeroSupplyWriter) Write([]byte) (int, error) { return 0, nil }

func TestSupplyPreparationPersistsAndVerifiesEverySignedNonce(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "c")
	if err := prepareCorpus(context.Background(), root, "../../scripts/gov5-perf/isolated-chain.json", base, 5, 3, 3); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseSupplyManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSupplyManifest(root)
	if err != nil || loaded.Digest != manifest.Digest {
		t.Fatalf("actual genesis/manifest binding failed: %v", err)
	}
	genesisRaw, err := os.ReadFile(filepath.Join(root, "genesis.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Descriptor.GenesisFileHash != types.Hash(sha256.Sum256(genesisRaw)) {
		t.Fatal("persisted genesis not bound")
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(genesisRaw, &genesis); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile(filepath.Join(root, "sender.seed"))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := checkSupplyAccounts(&genesis, seed, manifest.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	signer := transaction.NewLondonSigner(uint256.NewInt(params.PerformanceChainID).ToBig())
	seen := make(map[[2]uint64]bool)
	for _, shard := range manifest.Shards {
		wire, err := os.ReadFile(filepath.Join(root, shard.File))
		if err != nil {
			t.Fatal(err)
		}
		if types.Hash(sha256.Sum256(wire)) != shard.SHA256 {
			t.Fatal("shard digest differs")
		}
		reader := bytes.NewReader(wire)
		var scratch []byte
		for seq := uint64(0); seq < shard.Frames; seq++ {
			frame, reusable, err := sharedexec.ReadCorpusFrame(reader, scratch)
			scratch = reusable
			if err != nil {
				t.Fatal(err)
			}
			sender, nonce, _, err := manifest.Descriptor.FrameRange(shard.Index, seq)
			if err != nil {
				t.Fatal(err)
			}
			var offset uint64
			if err := sharedexec.WalkCorpusPayload(frame.Payload, frame.Transactions, func(raw []byte) error {
				tx, err := transaction.DecodeEthereumTransaction(raw)
				if err != nil {
					return err
				}
				actual, err := signer.Sender(tx)
				if err != nil {
					return err
				}
				pair := [2]uint64{uint64(sender), nonce + offset}
				if actual != crypto.PubkeyToAddress(keys[sender].PublicKey) || tx.Nonce() != pair[1] || seen[pair] {
					return errors.New("signed nonce repeated or mutated")
				}
				seen[pair] = true
				offset++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if reader.Len() != 0 {
			t.Fatal("trailing shard data")
		}
	}
	if len(seen) != 15 {
		t.Fatal("preparation omitted signed sender nonce")
	}
	// The last sender is checked too; a correct first sender cannot hide a
	// stale or unfunded account elsewhere in the descriptor.
	address := crypto.PubkeyToAddress(keys[len(keys)-1].PublicKey)
	account := genesis.Alloc[address]
	account.Nonce++
	genesis.Alloc[address] = account
	if _, err := checkSupplyAccounts(&genesis, seed, manifest.Descriptor); err == nil {
		t.Fatal("nonzero last sender genesis nonce accepted")
	}
	if err := os.WriteFile(filepath.Join(root, "genesis.json"), append(genesisRaw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSupplyManifest(root); err == nil {
		t.Fatal("changed actual genesis file accepted")
	}
}
