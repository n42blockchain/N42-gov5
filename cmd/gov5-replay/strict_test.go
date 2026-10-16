package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

type strictTestAdmitter struct {
	epoch [16]byte
	calls int
	admit func(context.Context, sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error)
}

func (c *strictTestAdmitter) Epoch() [16]byte { return c.epoch }
func (c *strictTestAdmitter) Close() error    { return nil }
func (c *strictTestAdmitter) Admit(ctx context.Context, f sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
	c.calls++
	return c.admit(ctx, f)
}
func strictTestFrame(t *testing.T) sharedexec.CorpusFrame {
	t.Helper()
	f, err := sharedexec.MakeCorpusFrame(sharedexec.CorpusFrameID{CorpusID: types.Hash{1}}, [][]byte{{1}, {2}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func strictTestAck(f sharedexec.CorpusFrame, epoch [16]byte) sharedexec.StrictIngressAck {
	return sharedexec.StrictIngressAck{Status: sharedexec.StrictIngressStatus{Epoch: epoch}, NewlyAdmitted: int(f.Transactions)}
}

func TestStrictSourceHashesEveryShardBeforePrefixAndKeepsDescriptors(t *testing.T) {
	root, m := supplyFixture(t)
	files, err := openStrictSources(context.Background(), root, m)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStrictSources(files)
	if len(files) != len(m.Shards) {
		t.Fatal("omitted shard integrity")
	}
	for i, file := range files {
		position, err := file.file.Seek(0, 1)
		if err != nil || position != 0 {
			t.Fatal("verified source not rewound")
		}
		frame, _, err := sharedexec.ReadCorpusFrame(file.file, nil)
		if err != nil || frame.Shard != uint16(i) {
			t.Fatal("verified descriptor not readable")
		}
	}
	path := filepath.Join(root, m.Shards[len(m.Shards)-1].File)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	failed, err := openStrictSources(context.Background(), root, m)
	if err == nil {
		closeStrictSources(failed)
		t.Fatal("late shard corruption ignored before prefix feed")
	}
	for _, file := range failed {
		if _, err := file.file.Stat(); err == nil {
			t.Fatal("failed integrity leaked source descriptor")
		}
	}
	if err := checkStrictSource(files[len(files)-1]); err == nil {
		t.Fatal("in-place modification after source verification ignored")
	}
}
func TestStrictStreamRetriesOnlyKnownZeroWithoutChangingFrame(t *testing.T) {
	frame := strictTestFrame(t)
	epoch := [16]byte{1}
	counters := new(strictStreamCounters)
	expected := append([]byte(nil), frame.Payload...)
	c := &strictTestAdmitter{epoch: epoch}
	c.admit = func(ctx context.Context, f sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		if f.CorpusFrameID != frame.CorpusFrameID || f.PayloadHash != frame.PayloadHash || !bytes.Equal(f.Payload, expected) {
			t.Fatal("retry modified original signature bytes/identity")
		}
		switch c.calls {
		case 1:
			return sharedexec.StrictIngressAck{}, sharedexec.ErrIngressCreditsExhausted
		case 2:
			return sharedexec.StrictIngressAck{}, sharedexec.ErrIngressSnapshotChanged
		default:
			return strictTestAck(f, epoch), nil
		}
	}
	accepted, err := strictAdmitFrame(context.Background(), context.Background(), c, frame, epoch, counters)
	if err != nil || !accepted || c.calls != 3 || counters.admitted.Load() != 2 || counters.busy.Load() != 1 || counters.snapshot.Load() != 1 {
		t.Fatalf("retry accounting %v %v %+v", accepted, err, counters.sample())
	}
}
func TestStrictStreamUnknownOrValidationFailureNeverReplays(t *testing.T) {
	for _, kind := range []string{"unknown", "nonce", "retry-nonzero", "duplicate", "welcome-epoch", "ack-epoch"} {
		t.Run(kind, func(t *testing.T) {
			frame := strictTestFrame(t)
			epoch := [16]byte{1}
			counters := new(strictStreamCounters)
			c := &strictTestAdmitter{epoch: epoch}
			if kind == "welcome-epoch" {
				c.epoch = [16]byte{2}
			}
			c.admit = func(ctx context.Context, f sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
				ack := strictTestAck(f, epoch)
				switch kind {
				case "unknown":
					return sharedexec.StrictIngressAck{}, sharedexec.ErrIngressUnknownOutcome
				case "nonce":
					return sharedexec.StrictIngressAck{}, errors.New("invalid nonce")
				case "retry-nonzero":
					return ack, sharedexec.ErrIngressSnapshotChanged
				case "duplicate":
					ack.NewlyAdmitted = 0
					ack.Duplicates = int(f.Transactions)
					return ack, nil
				case "ack-epoch":
					ack.Status.Epoch = [16]byte{2}
					return ack, nil
				}
				return ack, nil
			}
			accepted, err := strictAdmitFrame(context.Background(), context.Background(), c, frame, epoch, counters)
			if err == nil || accepted || c.calls > 1 || counters.admitted.Load() != 0 {
				t.Fatalf("fatal outcome replayed %s calls%d %v", kind, c.calls, err)
			}
			if kind == "welcome-epoch" && c.calls != 0 {
				t.Fatal("sent before pinned welcome epoch")
			}
		})
	}
}
func TestStrictStopDispatchAllowsInFlightFinalAck(t *testing.T) {
	frame := strictTestFrame(t)
	epoch := [16]byte{1}
	dispatch, stop := context.WithCancel(context.Background())
	defer stop()
	c := &strictTestAdmitter{epoch: epoch, admit: func(ctx context.Context, f sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		stop()
		if ctx.Err() != nil {
			t.Fatal("measurement canceled granted request")
		}
		return strictTestAck(f, epoch), nil
	}}
	counters := new(strictStreamCounters)
	accepted, err := strictAdmitFrame(context.Background(), dispatch, c, frame, epoch, counters)
	if err != nil || !accepted || counters.admitted.Load() != 2 {
		t.Fatal("lost final ACK at stop boundary")
	}
	if accepted, err := strictAdmitFrame(context.Background(), dispatch, c, frame, epoch, counters); err != nil || accepted || c.calls != 1 {
		t.Fatal("dispatch continued after boundary")
	}
}
func TestStrictSnapshotRetryHasBoundedDeadline(t *testing.T) {
	frame := strictTestFrame(t)
	epoch := [16]byte{1}
	c := &strictTestAdmitter{epoch: epoch, admit: func(ctx context.Context, f sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		return sharedexec.StrictIngressAck{}, sharedexec.ErrIngressSnapshotChanged
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	counters := new(strictStreamCounters)
	accepted, err := strictAdmitFrame(ctx, context.Background(), c, frame, epoch, counters)
	if err == nil || accepted || !errors.Is(err, context.DeadlineExceeded) || counters.admitted.Load() != 0 || c.calls < 1 {
		t.Fatalf("snapshot retry did not stop: %v", err)
	}
}
func TestStrictPumpStopsAtAcknowledgedPrefix(t *testing.T) {
	root, m := supplyFixture(t)
	sources, err := openStrictSources(context.Background(), root, m)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStrictSources(sources)
	dispatch, stop := context.WithCancel(context.Background())
	defer stop()
	epoch := [16]byte{1}
	c := &strictTestAdmitter{epoch: epoch, admit: func(ctx context.Context, f sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		stop()
		return strictTestAck(f, epoch), nil
	}}
	ready := make(chan error, 1)
	start := make(chan struct{})
	progress := new(strictShardProgress)
	counters := new(strictStreamCounters)
	pacer := &supplyPacer{rate: 1_000_000_000}
	done := make(chan error, 1)
	go func() {
		done <- strictPumpShard(context.Background(), dispatch, sources[0], m, 0, c, epoch, pacer, counters, ready, start, progress)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	pacer.start = time.Now()
	close(start)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if progress.Sequence != 1 || progress.Transactions != 2 || progress.Exhausted || c.calls != 1 || counters.read.Load() != 2 {
		t.Fatalf("read/sent beyond bounded prefix: %+v %+v", progress, counters.sample())
	}
}
func TestStrictWindowAndDrainUseOneCanonicalCounterAndOwnerCredits(t *testing.T) {
	start := time.Now()
	rate, err := strictWindowRate(start, start.Add(20*time.Second), 100, 300)
	if err != nil || rate != 10 {
		t.Fatal("canonical delta rate")
	}
	if _, err := strictWindowRate(start, start, 100, 300); err == nil {
		t.Fatal("zero duration accepted")
	}
	if _, err := strictWindowRate(start, start.Add(time.Second), 300, 100); err == nil {
		t.Fatal("counter regression accepted")
	}
	owner := sharedexec.StrictIngressStatus{NewlyAdmitted: 6, CanonicalHash: types.Hash{1}, CanonicalNumber: 2}
	statuses := make([]sharedvalidator.StatusResult, 7)
	for i := range statuses {
		statuses[i].CanonicalProgress = sharedvalidator.CanonicalProgress{Number: 2, BlockHash: owner.CanonicalHash, CommittedExecutedTransactions: 6}
	}
	if !strictDrainMatches(statuses, owner, 6) {
		t.Fatal("matching canonical/owner evidence rejected")
	}
	owner.PendingTransactions = 1
	if strictDrainMatches(statuses, owner, 6) {
		t.Fatal("local queues zero substituted for owner credit release")
	}
	owner.PendingTransactions = 0
	statuses[6].CommittedExecutedTransactions = 5
	if strictDrainMatches(statuses, owner, 6) {
		t.Fatal("summed or ignored a validator counter")
	}
}
func TestStrictAttemptNamesNeverOverwriteOrEscape(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../escape", "/absolute", "A", strings.Repeat("a", 33)} {
		if _, err := strictAttempt(root, name); err == nil {
			t.Fatal("unsafe attempt accepted")
		}
	}
	path, err := strictAttempt(root, "q1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "evidence"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := strictAttempt(root, "q1"); err == nil {
		t.Fatal("existing attempt overwritten")
	}
}

func TestStrictFirstFrameFailureReleasesOtherReadyWorkers(t *testing.T) {
	root, m := supplyFixture(t)
	path := filepath.Join(root, m.Shards[0].File)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[0] = 'X'
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	m.Shards[0].SHA256 = types.Hash(sha256.Sum256(raw))
	sources, err := openStrictSources(context.Background(), root, m)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStrictSources(sources)
	dispatch, stop := context.WithCancel(context.Background())
	defer stop()
	epoch := [16]byte{1}
	ready := make(chan error, len(m.Shards))
	start := make(chan struct{})
	done := make(chan error, len(m.Shards))
	var calls atomic.Int64
	pacer := &supplyPacer{start: time.Now(), rate: 1_000_000_000}
	counters := new(strictStreamCounters)
	for i := range m.Shards {
		go func(i int) {
			c := &strictTestAdmitter{epoch: epoch, admit: func(ctx context.Context, f sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
				calls.Add(1)
				return strictTestAck(f, epoch), nil
			}}
			progress := new(strictShardProgress)
			err := strictPumpShard(context.Background(), dispatch, sources[i], m, uint16(i), c, epoch, pacer, counters, ready, start, progress)
			if err != nil {
				stop()
			}
			done <- err
		}(i)
	}
	failures := 0
	for range m.Shards {
		select {
		case err := <-ready:
			if err != nil {
				failures++
				stop()
			}
		case <-time.After(time.Second):
			t.Fatal("ready worker stranded")
		}
	}
	for range m.Shards {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("start worker stranded after peer read failure")
		}
	}
	if failures == 0 || calls.Load() != 0 {
		t.Fatal("bad first frame did not stop every sender before dispatch")
	}
}

func TestStrictRuntimeBindsSchemaSourceAndActualGenesisFile(t *testing.T) {
	source, m := supplyFixture(t)
	base := t.TempDir()
	root := filepath.Join(base, "r")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	// This isolates metadata validation; the CLI first obtains m from the real
	// genesis-aware loadSupplyManifest path. No execution is simulated here.
	raw := []byte("exact source genesis bytes\n")
	sum := sha256.Sum256(raw)
	m.Descriptor.GenesisFileHash = types.Hash(sum)
	m.Descriptor.NonceStart = 0
	m.CorpusID, _ = m.Descriptor.ID()
	m.Digest = types.Hash{9}
	var runtime strictRuntime
	runtime.SchemaVersion = 2
	runtime.Status = "provisioned-not-initialized"
	runtime.Root = root
	runtime.Genesis = filepath.Join(root, "genesis.json")
	runtime.GenesisSHA256 = hex.EncodeToString(sum[:])
	runtime.Identity = m.Descriptor.Identity
	runtime.OwnerDir = filepath.Join(root, "e")
	runtime.OwnerSocket = filepath.Join(root, "e", "execution-rpc", "execution.sock")
	runtime.OwnerParallelEVM = true
	runtime.Transactions = "ethereum-secp256k1"
	runtime.GasPrice = "1"
	runtime.Supply.Root = source
	runtime.Supply.ManifestSHA256 = strings.TrimPrefix(m.Digest.Hex(), "0x")
	runtime.Supply.CorpusID = m.CorpusID
	runtime.Supply.GenesisFileSHA256 = runtime.GenesisSHA256
	runtime.Supply.BlockIntervalMS = 100
	for i := 0; i < 7; i++ {
		var record = struct {
			Index     int    `json:"index"`
			Address   string `json:"address"`
			PeerID    string `json:"peer_id"`
			DataDir   string `json:"data_dir"`
			Config    string `json:"config"`
			APISocket string `json:"api_socket"`
			TCPPort   int    `json:"tcp_port"`
		}{Index: i, Address: fmt.Sprintf("0x%040x", i+1), PeerID: fmt.Sprintf("test%d", i), DataDir: filepath.Join(root, fmt.Sprintf("v%d", i)), TCPPort: 28000 + i}
		record.Config = filepath.Join(record.DataDir, "node.json")
		record.APISocket = filepath.Join(record.DataDir, "validator-rpc", "validator.sock")
		runtime.Validators = append(runtime.Validators, record)
	}
	if err := os.WriteFile(runtime.Genesis, raw, 0600); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "run.json")
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadStrictRuntime(path, source, base, m)
	if err != nil || got.Supply.CorpusID != m.CorpusID {
		t.Fatalf("schema2 identity decode failed: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document["load_keys"] = "forbidden-v1-field"
	bad, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStrictRuntime(path, source, base, m); err == nil {
		t.Fatal("schema2 silently accepted sender keys")
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtime.Genesis, append(raw, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStrictRuntime(path, source, base, m); err == nil {
		t.Fatal("different runtime genesis bytes accepted")
	}
}

func TestStrictCorpusAvailabilityUsesFirstShardWindowBoundary(t *testing.T) {
	if strictCorpusAvailable(5, 100, 19*time.Second, 20*time.Second) {
		t.Fatal("one exhausted shard cannot be hidden by unconsumed other shards")
	}
	if strictCorpusAvailable(5, 100, 20*time.Second, 20*time.Second) {
		t.Fatal("EOF at window boundary is not spare supply")
	}
	if !strictCorpusAvailable(5, 100, 21*time.Second, 20*time.Second) {
		t.Fatal("in-flight completion after window must not invalidate earlier supply")
	}
	if !strictCorpusAvailable(5, 100, 0, 20*time.Second) || strictCorpusAvailable(100, 100, 0, 20*time.Second) {
		t.Fatal("window ACK count supply bound differs")
	}
}

func TestStrictPumpRecordsFullShardExhaustion(t *testing.T) {
	root, m := supplyFixture(t)
	sources, err := openStrictSources(context.Background(), root, m)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStrictSources(sources)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	epoch := [16]byte{1}
	client := &strictTestAdmitter{epoch: epoch, admit: func(ctx context.Context, frame sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		return strictTestAck(frame, epoch), nil
	}}
	ready := make(chan error, 1)
	start := make(chan struct{})
	progress := new(strictShardProgress)
	counters := new(strictStreamCounters)
	pacer := &supplyPacer{rate: 1_000_000_000}
	done := make(chan error, 1)
	go func() {
		done <- strictPumpShard(ctx, ctx, sources[0], m, 0, client, epoch, pacer, counters, ready, start, progress)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	pacer.start = time.Now()
	close(start)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("full shard did not finish")
	}
	if !progress.Exhausted || progress.Sequence != m.Shards[0].Frames || progress.Transactions != m.Shards[0].Transactions || counters.admitted.Load() != m.Shards[0].Transactions || counters.exhaustedShards.Load() != 1 || counters.firstExhaustedNanos.Load() <= 0 {
		t.Fatalf("full shard EOF evidence missing: %+v %+v", progress, counters.sample())
	}
}

func TestStrictPythonLauncherResultContract(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/gov5-perf/testdata/strict-result.json")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result strictRunResult
	if err := decoder.Decode(&result); err != nil {
		t.Fatal("Python launcher fixture differs from actual Go result JSON:", err)
	}
	if !result.Passed || result.WindowStart == nil || result.WindowEnd == nil || result.WindowStart.Canonical.CommittedExecutedTransactions != 1 || result.WindowEnd.Canonical.CommittedExecutedTransactions != 5 || result.CanonicalWindowDelta != 4 || result.Stream.Admitted != 8 || len(result.Final) != 7 {
		t.Fatal("cross-language canonical window/drain fixture differs")
	}
}
