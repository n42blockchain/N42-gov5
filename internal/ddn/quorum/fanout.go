// Package quorum performs bounded, independent provider calls.
package quorum

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sort"
	"sync"
	"time"

	metrics "github.com/n42blockchain/N42/common/metrics"
	chain "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ddn/aggregate"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	d "github.com/n42blockchain/N42/internal/ddn/types"
)

type Member struct {
	Provider provider.DecisionProvider
	Price    uint64
}

// Group uses all configured members; clients cannot silently downgrade quorum.
// A shared semaphore bounds sidecar calls across concurrent requests.
type Group struct {
	members  []Member
	identity provider.Identity
	permits  chan struct{}
	wg       sync.WaitGroup
}

func New(did string, members []Member, maxParallel int) (*Group, error) {
	if did == "" || len(members) < 1 || len(members) > 16 || maxParallel < 1 || maxParallel > 16 {
		return nil, errors.New("invalid DDN provider group")
	}
	ms := append([]Member{}, members...)
	for _, m := range ms {
		if m.Provider == nil {
			return nil, errors.New("nil quorum provider")
		}
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Provider.Identity().DID < ms[j].Provider.Identity().DID })
	var ids []provider.Identity
	seen := map[string]bool{}
	for _, m := range ms {
		id := m.Provider.Identity()
		if id.DID == "" || seen[id.DID] || id.ModelHash == (chain.Hash{}) {
			return nil, errors.New("quorum requires unique providers and pinned models")
		}
		seen[id.DID] = true
		ids = append(ids, id)
	}
	b, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	tasks := append([]string{}, ids[0].Tasks...)
	schemas := append([]string{}, ids[0].Schemas...)
	for _, id := range ids[1:] {
		tasks = intersection(tasks, id.Tasks)
		schemas = intersection(schemas, id.Schemas)
	}
	return &Group{members: ms, identity: provider.Identity{DID: did, Model: "DDN-quorum-v1", ModelVersion: "1", ModelHash: crypto.Keccak256Hash(b), Family: "aggregate", Tasks: tasks, Schemas: schemas}, permits: make(chan struct{}, maxParallel)}, nil
}
func intersection(a, b []string) []string {
	var out []string
	for _, v := range a {
		for _, s := range b {
			if v == s {
				out = append(out, v)
				break
			}
		}
	}
	return out
}
func (g *Group) Identity() provider.Identity {
	id := g.identity
	id.Tasks = append([]string{}, id.Tasks...)
	id.Schemas = append([]string{}, id.Schemas...)
	return id
}
func (g *Group) ValidateRequest(r d.DecisionRequest) error {
	if int(r.Quorum) != len(g.members) {
		return errors.New("request quorum must match the configured provider group")
	}
	budget, ok := new(big.Int).SetString(r.MaxCost, 10)
	if !ok || budget.Sign() < 0 {
		return errors.New("invalid quorum budget")
	}
	total := new(big.Int)
	for _, m := range g.members {
		total.Add(total, new(big.Int).SetUint64(m.Price))
	}
	if total.Cmp(budget) > 0 {
		return errors.New("quorum exceeds request budget")
	}
	return nil
}
func (g *Group) Decide(ctx context.Context, r d.DecisionRequest, input string) (d.DecisionResult, error) {
	result, _, err := g.DecideWithEvidence(ctx, r, input)
	return result, err
}
func (g *Group) DecideWithEvidence(ctx context.Context, r d.DecisionRequest, input string) (d.DecisionResult, chain.Hash, error) {
	if err := g.ValidateRequest(r); err != nil {
		return d.DecisionResult{}, chain.Hash{}, err
	}
	type response struct {
		index int
		out   aggregate.Outcome
	}
	ch := make(chan response, len(g.members))
	out := make([]aggregate.Outcome, len(g.members))
	for i, m := range g.members {
		id := m.Provider.Identity()
		out[i] = aggregate.Outcome{ProviderDID: id.DID, ModelHash: id.ModelHash, Family: id.Family, Failed: true}
		g.wg.Add(1)
		go func(index int, member Member, identity provider.Identity) {
			defer g.wg.Done()
			o := aggregate.Outcome{ProviderDID: identity.DID, ModelHash: identity.ModelHash, Family: identity.Family, Failed: true}
			defer func() {
				if recover() != nil {
					o.Failed = true
				}
				ch <- response{index, o}
			}()
			select {
			case g.permits <- struct{}{}:
				defer func() { <-g.permits }()
			case <-ctx.Done():
				return
			}
			if ctx.Err() != nil {
				return
			}
			started := time.Now()
			result, err := member.Provider.Decide(ctx, r, input)
			metrics.GetOrCreateHistogram("ddn_quorum_provider_latency_seconds").Observe(time.Since(started).Seconds())
			if err == nil && ctx.Err() == nil {
				o.Result = result
				o.Failed = false
			}
		}(i, m, id)
	}
	remaining := len(out)
	for remaining > 0 {
		select {
		case response := <-ch:
			out[response.index] = response.out
			remaining--
		case <-ctx.Done():
			remaining = 0
		}
	}
	combined, err := aggregate.Combine(r, out)
	if err != nil {
		return d.DecisionResult{}, chain.Hash{}, err
	}
	if combined.Disagreement {
		metrics.GetOrCreateCounter("ddn_quorum_disagreements_total", false).Inc()
	}
	return combined.Result, combined.EvidenceCommitment, nil
}

// Wait is called after gateway cancellation and worker shutdown, so no new
// fan-out goroutines can be added while waiting.
func (g *Group) Wait() { g.wg.Wait() }
