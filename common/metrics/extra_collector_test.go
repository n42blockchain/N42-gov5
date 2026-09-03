package prometheus

import (
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"

	_ "github.com/n42blockchain/N42/lib/kv" // populates db_pgops et al in lib/metrics' Set
	libmetrics "github.com/n42blockchain/N42/lib/metrics"
)

// lib/metrics keeps its own default Set, and nothing registered it: its
// Setup() is the only thing that does and Setup() has no callers. So the whole
// storage-layer family -- db_pgops{phase=...}, kvcache, txpool, layered, disk,
// mem -- was updated on every MDBX commit and served nowhere. This asserts the
// wiring that fixes it: a gauge created through lib/metrics, with a label in
// the name the way kv_interface.go writes them, survives a Gather.
//
// The registry here is built directly rather than through Handler() because
// Handler builds its gatherer under a sync.Once, so whether a registration is
// visible would depend on test ordering within the package.
func TestLibMetricsSetIsGatherable(t *testing.T) {
	g := libmetrics.GetOrCreateGauge(`test_pgops{phase="spill"}`)
	if g == nil {
		t.Fatal("GetOrCreateGauge returned nil")
	}
	g.SetUint64(4242)

	reg := prom.NewRegistry()
	if err := reg.Register(libmetrics.DefaultSet()); err != nil {
		t.Fatalf("lib/metrics default Set must be registerable as a Collector: %v", err)
	}

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sb strings.Builder
	enc := expfmt.NewEncoder(&sb, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	out := sb.String()
	if !strings.Contains(out, "test_pgops") {
		t.Fatalf("a gauge created through lib/metrics must reach the served output; got:\n%s", out)
	}
	if !strings.Contains(out, "spill") {
		t.Errorf("the label must survive too -- kv writes its names as db_pgops{phase=\"spill\"}; got:\n%s", out)
	}
	if !strings.Contains(out, "4242") {
		t.Errorf("the value must survive; got:\n%s", out)
	}
}

// A nil collector must not panic or poison the extras list: callers wire this
// from optional subsystems.
func TestRegisterCollectorIgnoresNil(t *testing.T) {
	before := len(extraCollectors)
	RegisterCollector(nil)
	if len(extraCollectors) != before {
		t.Fatalf("nil collector must be ignored, extras grew %d -> %d", before, len(extraCollectors))
	}
}

// The two packages are parallel implementations each with its own default Set.
// If any metric name appears in both, registering the second into one registry
// fails and -- because RegisterCollector logs and skips rather than aborting --
// the storage family would silently go on being served nowhere, which is the
// exact failure this wiring exists to end. lib/kv is imported for its side
// effects so the real db_pgops names are present rather than a test gauge.
func TestBothDefaultSetsCoexistInOneRegistry(t *testing.T) {
	reg := prom.NewRegistry()
	if err := reg.Register(defaultSet); err != nil {
		t.Fatalf("common/metrics default Set: %v", err)
	}
	if err := reg.Register(libmetrics.DefaultSet()); err != nil {
		t.Fatalf("lib/metrics default Set must coexist with common/metrics' in one registry, "+
			"otherwise RegisterCollector skips it and db_pgops stays unserved: %v", err)
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather with both sets: %v", err)
	}
	var names []string
	for _, mf := range mfs {
		names = append(names, mf.GetName())
	}
	joined := strings.Join(names, " ")
	if !strings.Contains(joined, "db_pgops") {
		t.Errorf("db_pgops must be present once lib/kv is loaded; served names were:\n%s", joined)
	}
}
