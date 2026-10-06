package prometheus

import (
	"reflect"
	"testing"

	vm "github.com/VictoriaMetrics/metrics"
)

func TestStandardRegistryRegisterGetUnregister(t *testing.T) {
	r := NewRegistry()
	c := vm.NewCounter("g41_registry_counter")

	if err := r.Register("g41_registry_counter", c); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	if err := r.Register("g41_registry_counter", c); err == nil {
		t.Fatal("expected DuplicateMetric error on second Register")
	} else if _, ok := err.(DuplicateMetric); !ok {
		t.Fatalf("expected DuplicateMetric, got %T", err)
	}

	if got := r.Get("g41_registry_counter"); got != c {
		t.Fatalf("Get returned %v, want %v", got, c)
	}
	if got := r.Get("missing"); got != nil {
		t.Fatalf("Get(missing) = %v, want nil", got)
	}

	var seen []string
	r.Each(func(name string, i interface{}) {
		seen = append(seen, name)
	})
	if len(seen) != 1 || seen[0] != "g41_registry_counter" {
		t.Fatalf("Each() saw %v", seen)
	}

	r.Unregister("g41_registry_counter")
	if got := r.Get("g41_registry_counter"); got != nil {
		t.Fatalf("expected metric to be gone after Unregister, got %v", got)
	}
}

func TestStandardRegistryGetOrRegister(t *testing.T) {
	r := NewRegistry()
	c := vm.NewCounter("g41_registry_getorreg")

	got := r.GetOrRegister("g41_registry_getorreg", c)
	if got != c {
		t.Fatalf("GetOrRegister returned %v, want %v", got, c)
	}

	// Second call should return the existing metric, not re-register.
	got2 := r.GetOrRegister("g41_registry_getorreg", vm.NewCounter("g41_registry_getorreg_other"))
	if got2 != c {
		t.Fatal("GetOrRegister should return the already-registered metric")
	}

	// Lazy instantiation via a func value.
	lazyName := "g41_registry_getorreg_lazy"
	lazyCounter := vm.NewCounter(lazyName)
	gotLazy := r.GetOrRegister(lazyName, func() interface{} { return lazyCounter })
	if gotLazy != lazyCounter {
		t.Fatalf("GetOrRegister(func) = %v, want %v", gotLazy, lazyCounter)
	}
}

func TestStandardRegistryUnregisterAll(t *testing.T) {
	r := NewRegistry()
	_ = r.Register("g41_registry_ua_a", vm.NewCounter("g41_registry_ua_a"))
	_ = r.Register("g41_registry_ua_b", vm.NewGauge("g41_registry_ua_b", func() float64 { return 0 }))

	r.UnregisterAll()

	var seen []string
	r.Each(func(name string, i interface{}) { seen = append(seen, name) })
	if len(seen) != 0 {
		t.Fatalf("expected no metrics after UnregisterAll, got %v", seen)
	}
}

func TestStandardRegistryRegisterUnknownTypeLogsAndSucceeds(t *testing.T) {
	r := NewRegistry()
	// A type not in the known switch: register() logs and returns nil error,
	// but does NOT store the metric (see registry.go register()).
	if err := r.Register("g41_registry_unknown", "not-a-metric"); err != nil {
		t.Fatalf("Register(unknown type) error: %v", err)
	}
	if got := r.Get("g41_registry_unknown"); got != nil {
		t.Fatalf("expected unknown-type metric not to be stored, got %v", got)
	}
}

func TestDuplicateMetricError(t *testing.T) {
	err := DuplicateMetric("foo")
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestPackageLevelGet(t *testing.T) {
	c := vm.NewCounter("g41_registry_pkg_get")
	if err := DefaultRegistry.Register("g41_registry_pkg_get", c); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	defer DefaultRegistry.Unregister("g41_registry_pkg_get")

	if got := Get("g41_registry_pkg_get"); got != c {
		t.Fatalf("Get() = %v, want %v", got, c)
	}
}

func TestParseMetricSimpleAndLabeled(t *testing.T) {
	name, labels, err := parseMetric("foo_bar")
	if err != nil {
		t.Fatalf("parseMetric(simple) error: %v", err)
	}
	if name != "foo_bar" || labels != nil {
		t.Fatalf("parseMetric(simple) = (%q, %v)", name, labels)
	}

	name, labels, err = parseMetric(`foo{bar="baz",aaa="b"}`)
	if err != nil {
		t.Fatalf("parseMetric(labeled) error: %v", err)
	}
	if name != "foo" {
		t.Fatalf("parseMetric name = %q, want foo", name)
	}
	want := map[string]string{"bar": "baz", "aaa": "b"}
	if !reflect.DeepEqual(map[string]string(labels), want) {
		t.Fatalf("parseMetric labels = %v, want %v", labels, want)
	}
}

func TestParseMetricErrors(t *testing.T) {
	cases := []string{
		"",                  // empty
		"1invalid",          // bad ident
		"foo{bar=\"baz\"",   // missing closing brace
		"foo{bar}",          // missing =
		"foo{1bad=\"x\"}",   // bad tag ident
		"foo{bar=baz}",      // missing opening quote
		"foo{bar=\"baz}",    // missing trailing quote
		"foo{bar=\"baz\" }", // missing comma after value (trailing space, no comma)
	}
	for _, c := range cases {
		if _, _, err := parseMetric(c); err == nil {
			t.Errorf("parseMetric(%q): expected error, got nil", c)
		}
	}
}

func TestParseMetricEscapedQuote(t *testing.T) {
	// An escaped quote inside the value must not terminate the string.
	_, labels, err := parseMetric(`foo{bar="a\"b"}`)
	if err != nil {
		t.Fatalf("parseMetric(escaped quote) error: %v", err)
	}
	if labels["bar"] == "" {
		t.Fatal("expected a non-empty escaped value")
	}
}

func TestParseMetricMultipleTagsWithSpace(t *testing.T) {
	_, labels, err := parseMetric(`foo{a="1", b="2"}`)
	if err != nil {
		t.Fatalf("parseMetric error: %v", err)
	}
	if labels["a"] != "1" || labels["b"] != "2" {
		t.Fatalf("unexpected labels: %v", labels)
	}
}

func TestValidateIdentViaParseMetric(t *testing.T) {
	if _, _, err := parseMetric("valid_ident:123"); err != nil {
		t.Fatalf("expected valid identifier to pass: %v", err)
	}
	if _, _, err := parseMetric("!!!"); err == nil {
		t.Fatal("expected invalid identifier to fail")
	}
}
