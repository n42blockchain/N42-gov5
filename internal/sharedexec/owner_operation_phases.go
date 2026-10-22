package sharedexec

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// Disjoint monotonic intervals for the existing shared owner's serialized
// operations. Nested validator/client intervals overlap these and are not CPU
// time to add together. No payload, signing key or arbitrary error is recorded.
type ownerOperationPhases struct {
	operation, active, lastPhase string
	started, changed             time.Time
	elapsed                      map[string]int64
	identity                     Identity
	hash, parent                 types.Hash
	number                       uint64
	duplicate                    bool
	transactions                 int
	verifiedSenderMemos          int
}

type ownerOperationPhaseRecord struct {
	SchemaVersion                                          int
	Operation, Outcome, LastPhase                          string
	StartedUnixMicros, TotalNs, AccountedNs, UnaccountedNs int64
	PhaseNs                                                map[string]int64
	Identity                                               Identity
	Hash, Parent                                           types.Hash
	Number                                                 uint64
	Duplicate                                              bool
	Transactions                                           int
	VerifiedSenderMemos                                    int
}

// Console logging uses Stringer while the JSON formatter retains the typed
// structure. Both forms preserve a complete JSON-decodable record.
func (r ownerOperationPhaseRecord) String() string {
	raw, err := json.Marshal(r)
	if err != nil {
		return `{"phase_record_encoding_error":true}`
	}
	return string(raw)
}

func newOwnerOperationPhases(operation string, server *Server) *ownerOperationPhases {
	now := time.Now()
	p := &ownerOperationPhases{operation: operation, started: now, changed: now, elapsed: make(map[string]int64)}
	if server != nil {
		p.identity = server.identity
	}
	return p
}
func (p *ownerOperationPhases) phase(name string) {
	if p != nil {
		p.phaseAt(name, time.Now())
	}
}
func (p *ownerOperationPhases) pause() { p.phase("") }
func (p *ownerOperationPhases) phaseAt(name string, at time.Time) {
	if p.active != "" {
		p.elapsed[p.active] += at.Sub(p.changed).Nanoseconds()
	}
	if name != "" {
		p.lastPhase = name
	}
	p.active, p.changed = name, at
}
func (p *ownerOperationPhases) record(at time.Time, err error) ownerOperationPhaseRecord {
	p.phaseAt("", at)
	outcome := "ok"
	if err != nil {
		outcome = "error"
		if errors.Is(err, context.Canceled) {
			outcome = "canceled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			outcome = "deadline"
		}
	}
	phases := make(map[string]int64, len(p.elapsed))
	var accounted int64
	for name, elapsed := range p.elapsed {
		phases[name] = elapsed
		accounted += elapsed
	}
	total := at.Sub(p.started).Nanoseconds()
	return ownerOperationPhaseRecord{SchemaVersion: 1, Operation: p.operation, Outcome: outcome, LastPhase: p.lastPhase, StartedUnixMicros: p.started.UnixMicro(), TotalNs: total, AccountedNs: accounted, UnaccountedNs: total - accounted, PhaseNs: phases, Identity: p.identity, Hash: p.hash, Parent: p.parent, Number: p.number, Duplicate: p.duplicate, Transactions: p.transactions, VerifiedSenderMemos: p.verifiedSenderMemos}
}
func (p *ownerOperationPhases) finish(err error) {
	record := p.record(time.Now(), err)
	log.Info("shared owner operation phases", "operation", record.Operation, "record", record)
}
