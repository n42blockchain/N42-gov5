// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Bounded in-memory store plus append-only JSONL log of equivocation evidence
// (profile-1 E15, RPC n42_equivocations). Each entry is verifiable standalone:
// both signed messages, both BLS signatures and the signer's public key.

package hotstuff

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Evidence kinds.
const (
	EvidenceKindVote       = "vote"
	EvidenceKindCommitVote = "commit_vote"
	EvidenceKindProposal   = "proposal"
)

// DefaultEquivocationCap bounds the in-memory evidence list.
const DefaultEquivocationCap = 4096

// SignedMessage is one half of an equivocation: the exact bytes that were
// signed and the signature over them.
type SignedMessage struct {
	Signed    string `json:"signed"`    // signed bytes, hex
	Signature string `json:"signature"` // BLS signature, hex
}

// EquivocationEvidence is one standalone-verifiable equivocation proof.
type EquivocationEvidence struct {
	View           uint64        `json:"view"`
	ValidatorIndex uint32        `json:"validatorIndex"`
	Hash1          string        `json:"hash1"`
	Hash2          string        `json:"hash2"`
	DetectedAt     int64         `json:"detectedAt"` // unix seconds
	Kind           string        `json:"kind"`
	PublicKey      string        `json:"publicKey"` // BLS public key, hex
	First          SignedMessage `json:"first"`
	Second         SignedMessage `json:"second"`
}

// EquivocationReport is the n42_equivocations RPC result.
type EquivocationReport struct {
	Total    uint64                 `json:"total"`
	Evidence []EquivocationEvidence `json:"evidence"`
}

// EquivocationStore keeps the newest evidence entries (bounded) and appends
// each one to a JSONL file when a path is configured.
type EquivocationStore struct {
	mu       sync.Mutex
	cap      int
	total    uint64
	list     []EquivocationEvidence
	seen     map[string]struct{}
	jsonl    string
	nowUnix  func() int64
	writeErr error
}

// NewEquivocationStore creates a store holding at most capacity entries.
func NewEquivocationStore(capacity int) *EquivocationStore {
	if capacity <= 0 {
		capacity = DefaultEquivocationCap
	}
	return &EquivocationStore{cap: capacity, seen: make(map[string]struct{}), nowUnix: func() int64 { return time.Now().Unix() }}
}

// SetJSONLPath sets the append-only evidence log (created on first evidence).
func (s *EquivocationStore) SetJSONLPath(path string) {
	s.mu.Lock()
	s.jsonl = path
	s.mu.Unlock()
}

func evHex(b []byte) string { return "0x" + hex.EncodeToString(b) }

func evidenceKey(ev *EquivocationEvidence) string {
	return fmt.Sprintf("%s/%d/%d/%s/%s", ev.Kind, ev.View, ev.ValidatorIndex, ev.Hash1, ev.Hash2)
}

// RecordOutput stores the evidence carried by an OutputEquivocationDetected.
// It returns false for duplicates and for outputs without signature detail.
func (s *EquivocationStore) RecordOutput(o EngineOutput) bool {
	if o.EvidenceKind == "" || len(o.Sig1) == 0 || len(o.Sig2) == 0 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := EquivocationEvidence{
		View:           uint64(o.View),
		ValidatorIndex: uint32(o.Validator),
		Hash1:          evHex(o.Hash1[:]),
		Hash2:          evHex(o.Hash2[:]),
		DetectedAt:     s.nowUnix(),
		Kind:           o.EvidenceKind,
		PublicKey:      evHex(o.PublicKey),
		First:          SignedMessage{Signed: evHex(o.Msg1), Signature: evHex(o.Sig1)},
		Second:         SignedMessage{Signed: evHex(o.Msg2), Signature: evHex(o.Sig2)},
	}
	key := evidenceKey(&ev)
	if _, dup := s.seen[key]; dup {
		return false
	}
	s.seen[key] = struct{}{}
	s.total++
	s.list = append(s.list, ev)
	if len(s.list) > s.cap {
		drop := s.list[0]
		delete(s.seen, evidenceKey(&drop))
		s.list = append([]EquivocationEvidence(nil), s.list[1:]...)
	}
	if s.jsonl != "" {
		s.writeErr = appendJSONL(s.jsonl, &ev)
	}
	return true
}

func appendJSONL(path string, ev *EquivocationEvidence) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// LastWriteError reports the most recent JSONL append failure, if any.
func (s *EquivocationStore) LastWriteError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeErr
}

// Report returns the total count and a copy of the retained evidence.
func (s *EquivocationStore) Report() EquivocationReport {
	s.mu.Lock()
	defer s.mu.Unlock()
	return EquivocationReport{Total: s.total, Evidence: append([]EquivocationEvidence{}, s.list...)}
}

// DefaultEquivocationStore is the process-wide store fed by the HotStuff
// service and served by the n42_equivocations RPC.
var DefaultEquivocationStore = NewEquivocationStore(DefaultEquivocationCap)
