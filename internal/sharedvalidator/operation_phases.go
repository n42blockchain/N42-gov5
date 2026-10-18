package sharedvalidator

import (
	"context"
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// operationPhases observes only this performance-only replica path. Durations
// are disjoint monotonic intervals within one operation; nested RPC/replica
// operations overlap their caller and must not be added as CPU time.
type operationPhases struct {
	operation, path, active, lastPhase string
	started, changed                   time.Time
	elapsed                            map[string]int64
	validator                          types.Address
	view                               uint64
	parent, hash                       types.Hash
	number                             uint64
	durable                            bool
	transactions, rawBytes             int
}

type operationPhaseRecord struct {
	SchemaVersion                       int
	Operation, Path, Outcome, LastPhase string
	StartedUnixMicros, TotalNs          int64
	AccountedNs, UnaccountedNs          int64
	PhaseNs                             map[string]int64
	Validator                           types.Address
	View, Number                        uint64
	Parent, Hash                        types.Hash
	Durable                             bool
	Transactions, RawBytes              int
}

func newOperationPhases(operation string, r *Replica) *operationPhases {
	now := time.Now()
	p := &operationPhases{operation: operation, started: now, changed: now, elapsed: make(map[string]int64)}
	if r != nil && r.engine != nil {
		p.validator, _ = r.engine.SignerKey()
	}
	return p
}

func (p *operationPhases) phase(name string) { p.phaseAt(name, time.Now()) }
func (p *operationPhases) pause()            { p.phase("") }

func (p *operationPhases) phaseAt(name string, at time.Time) {
	if p.active != "" {
		p.elapsed[p.active] += at.Sub(p.changed).Nanoseconds()
	}
	if name != "" {
		p.lastPhase = name
	}
	p.active, p.changed = name, at
}

func (p *operationPhases) record(at time.Time, err error) operationPhaseRecord {
	last := p.lastPhase
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
	return operationPhaseRecord{SchemaVersion: 1, Operation: p.operation, Path: p.path, Outcome: outcome, LastPhase: last,
		StartedUnixMicros: p.started.UnixMicro(), TotalNs: total, AccountedNs: accounted, UnaccountedNs: total - accounted,
		PhaseNs: phases, Validator: p.validator, View: p.view, Number: p.number, Parent: p.parent, Hash: p.hash,
		Transactions: p.transactions, RawBytes: p.rawBytes, Durable: p.durable}
}

func (p *operationPhases) finish(err error) {
	// The log itself is outside the measured interval. No transaction bytes,
	// signing keys or arbitrary error text enter this bounded record.
	record := p.record(time.Now(), err)
	log.Info("shared validator operation phases", "operation", record.Operation, "record", record)
}
