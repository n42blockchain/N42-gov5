package sharedexec

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

const maxOwnerTraceEvents = 8192
const maxOwnerTraceEventsPerBlock = 64

// Trace records use one process monotonic origin. Different processes may
// correlate hashes and epochs, but may not subtract their mono_ns values.
// Seq is enqueue order; writer intervals are enqueued after DB.Update returns,
// so interval comparisons must use mono_ns rather than seq.
// A missing summary or nonzero dropped/write_errors makes interval evidence
// incomplete, so a mechanism check must report inconclusive rather than PASS.
// The .complete sidecar is written only after the trace is synced and closed;
// an unpaired summary can never authorize acceptance.
type ownerTraceRecord struct {
	Kind       string     `json:"kind"`
	Schema     int        `json:"schema"`
	Run        string     `json:"run"`
	PID        int        `json:"pid"`
	Seq        uint64     `json:"seq"`
	MonoNS     int64      `json:"mono_ns"`
	Event      string     `json:"event,omitempty"`
	Origin     string     `json:"origin,omitempty"`
	Outcome    string     `json:"outcome,omitempty"`
	Anchor     types.Hash `json:"anchor_hash,omitempty"`
	Number     uint64     `json:"number,omitempty"`
	View       uint64     `json:"view,omitempty"`
	Epoch      string     `json:"epoch,omitempty"`
	Binding    types.Hash `json:"binding,omitempty"`       // speculative lease only
	Prepared   types.Hash `json:"prepared_hash,omitempty"` // public prepared header
	Execution  types.Hash `json:"execution_id,omitempty"`  // lease or ordinary template hash
	ResultHash types.Hash `json:"result_hash,omitempty"`   // built or imported block
	TxCount    int        `json:"tx_count,omitempty"`
	Accepted   uint64     `json:"accepted"`
	Dropped    uint64     `json:"dropped"`
	WriteErrs  uint64     `json:"write_errors"`
}

type ownerTraceBlock struct {
	parent types.Hash
	number uint64
	view   uint64
	epoch  string
}

type ownerChildTrace struct {
	mu       sync.Mutex
	file     *os.File
	start    time.Time
	run      string
	events   chan ownerTraceRecord
	done     chan struct{}
	perBlock map[ownerTraceBlock]uint8
	accepted uint64
	dropped  uint64
	writeErr uint64
	closed   bool
	err      error
}

type ownerTraceCompletion struct {
	Schema    int    `json:"schema"`
	Run       string `json:"run"`
	PID       int    `json:"pid"`
	SHA256    string `json:"sha256"`
	Accepted  uint64 `json:"accepted"`
	Dropped   uint64 `json:"dropped"`
	WriteErrs uint64 `json:"write_errors"`
}

// EnableOwnerChildTrace must be called before the owner starts. The file is
// fresh, private and independent of the pipeline switch, so OFF and ON legs
// can carry the same observation cost.
func (s *Server) EnableOwnerChildTrace(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("owner child trace requires absolute path")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.closed || s.trace != nil {
		return errors.New("owner child trace requires unused owner")
	}
	if _, err := os.Lstat(path + ".complete"); err == nil {
		return errors.New("owner child trace completion path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	t := &ownerChildTrace{file: f, start: time.Now(), run: hex.EncodeToString(id[:]), events: make(chan ownerTraceRecord, 1024), done: make(chan struct{}), perBlock: make(map[ownerTraceBlock]uint8)}
	s.trace = t
	go t.write()
	return nil
}

func (t *ownerChildTrace) record(at time.Time, event, outcome string, parent types.Hash, number, view uint64, epoch [16]byte, binding types.Hash, txCount int, keys ...types.Hash) {
	if t == nil {
		return
	}
	key := ownerTraceBlock{parent: parent, number: number, view: view, epoch: hex.EncodeToString(epoch[:])}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.accepted >= maxOwnerTraceEvents || t.perBlock[key] >= maxOwnerTraceEventsPerBlock || len(t.events) == cap(t.events) {
		t.dropped++
		return
	}
	t.accepted++
	t.perBlock[key]++
	record := ownerTraceRecord{Kind: "event", Schema: 1, Run: t.run, PID: os.Getpid(), Seq: t.accepted, MonoNS: at.Sub(t.start).Nanoseconds(), Event: event, Origin: ownerTraceOrigin(event), Outcome: outcome, Anchor: parent, Number: number, View: view, Epoch: key.epoch, Binding: binding, TxCount: txCount}
	if len(keys) > 0 {
		record.Prepared = keys[0]
	}
	if len(keys) > 1 {
		record.Execution = keys[1]
	}
	if len(keys) > 2 {
		record.ResultHash = keys[2]
	}
	t.events <- record
}

func ownerTraceOrigin(event string) string {
	switch {
	case strings.HasPrefix(event, "hint_"), strings.HasPrefix(event, "evm_speculative_"), event == "take_speculative", event == "consume_speculative", event == "speculative_fallback":
		return "speculative"
	case strings.HasPrefix(event, "evm_ordinary_"), event == "build_result":
		return "build"
	case strings.HasPrefix(event, "import_"):
		return "import"
	case strings.HasPrefix(event, "db_update_"), event == "ingress_reconcile_done", event == "commit_response":
		return "canonical"
	default:
		return "unknown"
	}
}

func (t *ownerChildTrace) mark(event, outcome string, anchor types.Hash, number, view uint64, epoch [16]byte, binding types.Hash, txCount int, keys ...types.Hash) {
	if t != nil {
		t.record(time.Now(), event, outcome, anchor, number, view, epoch, binding, txCount, keys...)
	}
}

func (t *ownerChildTrace) write() {
	defer close(t.done)
	encoder := json.NewEncoder(t.file)
	for record := range t.events {
		if encoder.Encode(record) != nil {
			t.mu.Lock()
			t.writeErr++
			t.mu.Unlock()
		}
	}
	t.mu.Lock()
	summary := ownerTraceRecord{Kind: "summary", Schema: 1, Run: t.run, PID: os.Getpid(), Accepted: t.accepted, Dropped: t.dropped, WriteErrs: t.writeErr}
	t.mu.Unlock()
	if t.writeErr != 0 {
		t.err = errors.New("owner child trace event write failed")
		_ = t.file.Close()
		return
	}
	if err := encoder.Encode(summary); err != nil {
		t.err = fmt.Errorf("owner child trace summary write: %w", err)
		_ = t.file.Close()
		return
	}
	if err := t.file.Sync(); err != nil {
		t.err = fmt.Errorf("owner child trace sync: %w", err)
		_ = t.file.Close()
		return
	}
	path := t.file.Name()
	if err := t.file.Close(); err != nil {
		t.err = fmt.Errorf("owner child trace close: %w", err)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		t.err = fmt.Errorf("owner child trace hash open: %w", err)
		return
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, f)
	closeErr := f.Close()
	if hashErr != nil || closeErr != nil {
		t.err = fmt.Errorf("owner child trace hash: %v; close: %v", hashErr, closeErr)
		return
	}
	completePath := path + ".complete"
	completion := ownerTraceCompletion{Schema: 1, Run: t.run, PID: os.Getpid(), SHA256: hex.EncodeToString(hash.Sum(nil)), Accepted: summary.Accepted, Dropped: summary.Dropped, WriteErrs: 0}
	complete, err := os.OpenFile(completePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.err = fmt.Errorf("owner child trace completion create: %w", err)
		return
	}
	if err := json.NewEncoder(complete).Encode(completion); err != nil {
		_ = complete.Close()
		_ = os.Remove(completePath)
		t.err = fmt.Errorf("owner child trace completion write: %w", err)
		return
	}
	if err := complete.Sync(); err != nil {
		_ = complete.Close()
		_ = os.Remove(completePath)
		t.err = fmt.Errorf("owner child trace completion sync: %w", err)
		return
	}
	if err := complete.Close(); err != nil {
		_ = os.Remove(completePath)
		t.err = fmt.Errorf("owner child trace completion close: %w", err)
		return
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		_ = os.Remove(completePath)
		t.err = fmt.Errorf("owner child trace directory open: %w", err)
		return
	}
	if err := dir.Sync(); err != nil {
		_ = dir.Close()
		_ = os.Remove(completePath)
		t.err = fmt.Errorf("owner child trace directory sync: %w", err)
		return
	}
	if err := dir.Close(); err != nil {
		_ = os.Remove(completePath)
		t.err = fmt.Errorf("owner child trace directory close: %w", err)
	}
}

func (t *ownerChildTrace) close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	if !t.closed {
		t.closed = true
		close(t.events)
	}
	t.mu.Unlock()
	<-t.done
	return t.err
}
